
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMovieBoxHost = "https://api6.aoneroom.com"
	defaultTimeout      = 20 * time.Second
	maxResponseSize     = 8 << 20
)

var movieBoxHosts = []string{
	"https://api6.aoneroom.com",
	"https://api5.aoneroom.com",
	"https://api4.aoneroom.com",
	"https://api4sg.aoneroom.com",
	"https://api3.aoneroom.com",
	"https://api6sg.aoneroom.com",
	"https://api.inmoviebox.com",
}

type MovieBoxClient struct {
	httpClient *http.Client
	baseURL    string
	token      string

	mu sync.RWMutex
}

type movieBoxResponse map[string]any

func NewMovieBoxClient() *MovieBoxClient {
	timeout := defaultTimeout

	if value := os.Getenv("MOVIEBOX_REQUEST_TIMEOUT"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil &&
			seconds > 0 && seconds <= 120 {
			timeout = time.Duration(seconds) * time.Second
		}
	}

	host := strings.TrimRight(
		strings.TrimSpace(os.Getenv("MOVIEBOX_API_BASE_URL")),
		"/",
	)

	if host == "" {
		host = defaultMovieBoxHost
	}

	return &MovieBoxClient{
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				if len(via) > 0 &&
					!strings.EqualFold(req.URL.Host, via[0].URL.Host) {
					return errors.New("cross-host redirect rejected")
				}
				return nil
			},
		},
		baseURL: host,
		token:   strings.TrimSpace(os.Getenv("MOVIEBOX_API_TOKEN")),
	}
}

// MovieBoxService compatibility.

func (m *MovieBoxClient) Search(
	ctx context.Context,
	query string,
	page int,
) (any, error) {
	query = strings.TrimSpace(query)

	if query == "" {
		return nil, errors.New("search query is required")
	}

	if page < 1 {
		page = 1
	}

	payload := map[string]any{
		"keyword":     query,
		"page":        page,
		"perPage":     15,
		"subjectType": 0,
	}

	var result movieBoxResponse

	err := m.postJSON(
		ctx,
		"/wefeed-mobile-bff/subject-api/search/v2",
		payload,
		&result,
	)

	if err != nil {
		return nil, err
	}

	return result, nil
}

func (m *MovieBoxClient) Details(
	ctx context.Context,
	subjectID string,
) (any, error) {
	if err := validateID(subjectID); err != nil {
		return nil, err
	}

	path := "/wefeed-mobile-bff/subject-api/get?subjectId=" +
		url.QueryEscape(subjectID)

	var result movieBoxResponse

	if err := m.getJSON(ctx, path, &result); err != nil {
		return nil, err
	}

	subject := findSubject(result)
	if subject == nil {
		subject = result
	}

	if readInt(subject, "subjectType", "stype") == 2 {
		seasonPath := "/wefeed-mobile-bff/subject-api/season-info?subjectId=" +
			url.QueryEscape(subjectID)

		var seasons movieBoxResponse

		if err := m.getJSON(ctx, seasonPath, &seasons); err == nil {
			subject["seasons"] = seasons
		}
	}

	return result, nil
}

func (m *MovieBoxClient) Episodes(
	ctx context.Context,
	subjectID string,
	season int,
) (any, error) {
	if err := validateID(subjectID); err != nil {
		return nil, err
	}

	if season < 0 || season > 1000 {
		return nil, errors.New("invalid season")
	}

	details, err := m.Details(ctx, subjectID)
	if err != nil {
		return nil, err
	}

	payload, ok := details.(map[string]any)
	if !ok {
		return nil, errors.New("invalid details response")
	}

	subject := findSubject(payload)
	if subject == nil {
		subject = payload
	}

	seasons := extractSeasons(subject)

	var selected any

	for _, item := range seasons {
		seasonNumber := readInt(item, "se", "season", "seasonNumber")

		if seasonNumber == season {
			selected = item
			break
		}
	}

	if selected == nil && len(seasons) > 0 {
		if season == 0 {
			selected = seasons[0]
		} else {
			return nil, fmt.Errorf("season %d not found", season)
		}
	}

	if selected == nil {
		return nil, errors.New("season information unavailable")
	}

	return map[string]any{
		"subject_id": subjectID,
		"season":     season,
		"data":       selected,
	}, nil
}

func (m *MovieBoxClient) Sources(
	ctx context.Context,
	subjectID string,
	season int,
	episode int,
) (any, error) {
	if err := validateID(subjectID); err != nil {
		return nil, err
	}

	if season < 0 || episode < 1 ||
		season > 1000 || episode > 100000 {
		return nil, errors.New("invalid season or episode")
	}

	playPath := fmt.Sprintf(
		"/wefeed-mobile-bff/subject-api/play-info/v2?subjectId=%s&se=%d&ep=%d",
		url.QueryEscape(subjectID),
		season,
		episode,
	)

	resourcePath := fmt.Sprintf(
		"/wefeed-mobile-bff/subject-api/resource?subjectId=%s&se=%d&ep=%d&page=1&perPage=50",
		url.QueryEscape(subjectID),
		season,
		episode,
	)

	type response struct {
		data movieBoxResponse
		err  error
	}

	playChannel := make(chan response, 1)
	resourceChannel := make(chan response, 1)

	go func() {
		var data movieBoxResponse
		err := m.getJSON(ctx, playPath, &data)
		playChannel <- response{data: data, err: err}
	}()

	go func() {
		var data movieBoxResponse
		err := m.getJSON(ctx, resourcePath, &data)
		resourceChannel <- response{data: data, err: err}
	}()

	play := <-playChannel
	resource := <-resourceChannel

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	sources := make([]map[string]any, 0)
	seen := make(map[string]bool)

	if play.err == nil {
		for _, item := range collectMaps(play.data) {
			if source := normalizeSource(item, season, episode); source != nil {
				addSource(&sources, seen, source)
			}
		}
	}

	if resource.err == nil {
		for _, item := range extractResourceItems(resource.data) {
			if source := normalizeSource(item, season, episode); source != nil {
				addSource(&sources, seen, source)
			}
		}
	}

	if play.err != nil && resource.err != nil {
		return nil, fmt.Errorf(
			"MovieBox playback metadata unavailable: play-info: %v; resource: %v",
			play.err,
			resource.err,
		)
	}

	return map[string]any{
		"provider":   "moviebox",
		"subject_id": subjectID,
		"season":     season,
		"episode":    episode,
		"status":     "success",
		"available":  len(sources) > 0,
		"sources":    sources,
	}, nil
}

func (m *MovieBoxClient) Subtitles(
	ctx context.Context,
	subjectID string,
	resourceID string,
) (any, error) {
	if err := validateID(subjectID); err != nil {
		return nil, err
	}

	if err := validateID(resourceID); err != nil {
		return nil, err
	}

	path := fmt.Sprintf(
		"/wefeed-mobile-bff/subject-api/get-ext-captions?subjectId=%s&resourceId=%s",
		url.QueryEscape(subjectID),
		url.QueryEscape(resourceID),
	)

	var result movieBoxResponse

	if err := m.getJSON(ctx, path, &result); err != nil {
		return nil, err
	}

	return map[string]any{
		"provider":    "moviebox",
		"subject_id":  subjectID,
		"resource_id": resourceID,
		"subtitles":   extractCaptions(result),
	}, nil
}

// HTTP transport.

func (m *MovieBoxClient) getJSON(
	ctx context.Context,
	path string,
	out any,
) error {
	return m.requestJSON(ctx, http.MethodGet, path, nil, out)
}

func (m *MovieBoxClient) postJSON(
	ctx context.Context,
	path string,
	body any,
	out any,
) error {
	return m.requestJSON(ctx, http.MethodPost, path, body, out)
}

func (m *MovieBoxClient) requestJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
	out any,
) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return errors.New("invalid API path")
	}

	encodedBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	hosts := m.hosts()

	var lastErr error

	for _, host := range hosts {
		if err := ctx.Err(); err != nil {
			return err
		}

		endpoint := strings.TrimRight(host, "/") + path

		reqBody := io.Reader(nil)
		if body != nil {
			reqBody = strings.NewReader(string(encodedBody))
		}

		req, err := http.NewRequestWithContext(
			ctx,
			method,
			endpoint,
			reqBody,
		)

		if err != nil {
			return err
		}

		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")

		m.mu.RLock()
		token := m.token
		m.mu.RUnlock()

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := m.httpClient.Do(req)
		if err != nil {
			lastErr = err

			var netErr net.Error
			if errors.As(err, &netErr) && !netErr.Timeout() {
				continue
			}
			continue
		}

		data, readErr := io.ReadAll(
			io.LimitReader(resp.Body, maxResponseSize+1),
		)
		resp.Body.Close()

		if readErr != nil {
			lastErr = readErr
			continue
		}

		if len(data) > maxResponseSize {
			lastErr = errors.New("API response exceeds size limit")
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("MovieBox HTTP %d", resp.StatusCode)

			if resp.StatusCode == http.StatusUnauthorized ||
				resp.StatusCode == http.StatusForbidden {
				return lastErr
			}

			if resp.StatusCode == http.StatusTooManyRequests ||
				resp.StatusCode >= 500 {
				continue
			}

			return lastErr
		}

		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("invalid MovieBox JSON: %w", err)
		}

		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("no MovieBox API hosts available")
	}

	return lastErr
}

func (m *MovieBoxClient) hosts() []string {
	custom := strings.TrimRight(m.baseURL, "/")

	if custom == "" {
		return []string{defaultMovieBoxHost}
	}

	parsed, err := url.Parse(custom)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return []string{defaultMovieBoxHost}
	}

	if custom != defaultMovieBoxHost {
		return []string{custom}
	}

	return movieBoxHosts
}

// Response adapters.

func findSubject(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}

	if subject, ok := data["subject"].(map[string]any); ok {
		return subject
	}

	if nested, ok := data["data"].(map[string]any); ok {
		if subject, ok := nested["subject"].(map[string]any); ok {
			return subject
		}
		return nested
	}

	return data
}

func extractSeasons(data map[string]any) []map[string]any {
	if data == nil {
		return nil
	}

	value := data["seasons"]

	if nested, ok := value.(map[string]any); ok {
		value = nested["seasons"]
	}

	return toMaps(value)
}

func extractResourceItems(data map[string]any) []map[string]any {
	if data == nil {
		return nil
	}

	if nested, ok := data["data"].(map[string]any); ok {
		data = nested
	}

	return toMaps(data["list"])
}

func collectMaps(data map[string]any) []map[string]any {
	result := make([]map[string]any, 0)

	var walk func(any, int)

	walk = func(value any, depth int) {
		if depth > 5 {
			return
		}

		switch current := value.(type) {
		case map[string]any:
			result = append(result, current)

			for key, child := range current {
				switch key {
				case "streams", "list", "data", "resources",
					"resourceList", "playList", "sources":
					walk(child, depth+1)
				}
			}

		case []any:
			for _, child := range current {
				walk(child, depth+1)
			}
		}
	}

	walk(data, 0)
	return result
}

func normalizeSource(
	item map[string]any,
	season int,
	episode int,
) map[string]any {
	if item == nil {
		return nil
	}

	itemSeason := readInt(item, "se", "season")
	itemEpisode := readInt(item, "ep", "episode")

	if itemSeason != 0 && itemSeason != season {
		return nil
	}

	if itemEpisode != 0 && itemEpisode != episode {
		return nil
	}

	rawURL := readString(
		item,
		"url",
		"resourceLink",
		"playUrl",
		"streamUrl",
		"link",
	)

	if !validMediaURL(rawURL) {
		return nil
	}

	return map[string]any{
		"id":       readString(item, "resourceId", "id"),
		"url":      rawURL,
		"quality":  readString(item, "resolution", "quality"),
		"codec":    readString(item, "codecName", "codec"),
		"language": readString(item, "lanName", "language", "lang"),
		"size":     readString(item, "size", "fileSize"),
		"filename": readString(item, "fileName", "filename", "title"),
		"headers":  map[string]string{},
		"type":     "unverified",
	}
}

func validMediaURL(raw string) bool {
	raw = strings.TrimSpace(raw)

	if raw == "" || len(raw) > 8192 {
		return false
	}

	u, err := url.Parse(raw)
	if err != nil {
		return false
	}

	if u.Scheme != "https" || u.Hostname() == "" {
		return false
	}

	host := strings.ToLower(u.Hostname())

	if host == "localhost" || host == "127.0.0.1" ||
		host == "::1" || strings.HasSuffix(host, ".local") {
		return false
	}

	lower := strings.ToLower(raw)

	blocked := []string{
		"notice",
		"deprecation",
		"placeholder",
		"error.mp4",
	}

	for _, word := range blocked {
		if strings.Contains(lower, word) {
			return false
		}
	}

	return true
}

func addSource(
	sources *[]map[string]any,
	seen map[string]bool,
	source map[string]any,
) {
	raw, _ := source["url"].(string)
	if raw == "" {
		return
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return
	}

	key := parsed.Host + parsed.Path

	if seen[key] {
		return
	}

	seen[key] = true
	*sources = append(*sources, source)
}

func extractCaptions(data map[string]any) []map[string]any {
	if nested, ok := data["data"].(map[string]any); ok {
		data = nested
	}

	items := toMaps(data["extCaptions"])
	if len(items) == 0 {
		items = toMaps(data["captions"])
	}

	result := make([]map[string]any, 0)

	for _, item := range items {
		rawURL := readString(item, "url", "captionUrl")

		if !validMediaURL(rawURL) {
			continue
		}

		result = append(result, map[string]any{
			"language": readString(item, "lanName", "lan", "language"),
			"url":      rawURL,
			"size":     readString(item, "size"),
		})
	}

	return result
}

// Generic helpers.

func toMaps(value any) []map[string]any {
	array, ok := value.([]any)
	if !ok {
		return nil
	}

	result := make([]map[string]any, 0, len(array))

	for _, item := range array {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}

	return result
}

func readString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func readInt(data map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := data[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case json.Number:
			number, _ := value.Int64()
			return int(number)
		case string:
			number, _ := strconv.Atoi(value)
			if number != 0 {
				return number
			}
		}
	}
	return 0
}

func validateID(value string) error {
	value = strings.TrimSpace(value)

	if value == "" || len(value) > 512 {
		return errors.New("invalid identifier")
	}

	if strings.ContainsAny(value, "\r\n") {
		return errors.New("invalid identifier")
	}

	return nil
}

var _ MovieBoxService = (*MovieBoxClient)(nil)
