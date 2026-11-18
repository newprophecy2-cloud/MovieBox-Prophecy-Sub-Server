
package main

import (
	"bytes"
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
	mu         sync.RWMutex
}

type movieBoxResponse map[string]any

type movieBoxHTTPError struct {
	StatusCode int
	Message    string
}

func (e *movieBoxHTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("MovieBox HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("MovieBox HTTP %d: %s", e.StatusCode, e.Message)
}

func NewMovieBoxClient() *MovieBoxClient {
	timeout := defaultTimeout

	if v := os.Getenv("MOVIEBOX_REQUEST_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 120 {
			timeout = time.Duration(n) * time.Second
		}
	}

	host := strings.TrimRight(
		strings.TrimSpace(os.Getenv("MOVIEBOX_API_BASE_URL")), "/",
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

// Search implements MovieBoxService.Search.
func (m *MovieBoxClient) Search(
	ctx context.Context,
	query string,
	page int,
) (any, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	if len(query) > 200 {
		return nil, errors.New("search query is too long")
	}
	if page < 1 {
		page = 1
	}
	if page > 10000 {
		return nil, errors.New("invalid page")
	}

	payload := map[string]any{
		"keyword":     query,
		"page":        page,
		"perPage":     15,
		"subjectType": 0,
	}

	var result movieBoxResponse
	if err := m.postJSON(
		ctx,
		"/wefeed-mobile-bff/subject-api/search/v2",
		payload,
		&result,
	); err != nil {
		return nil, err
	}

	return unwrapData(result), nil
}

// Details implements MovieBoxService.Details.
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
		return nil, errors.New("MovieBox returned no subject details")
	}

	// Official MovieBox uses subjectType=2 for series.
	if readInt(subject, "subjectType", "stype") == 2 {
		seasonPath := "/wefeed-mobile-bff/subject-api/season-info?subjectId=" +
			url.QueryEscape(subjectID)

		var seasonResponse movieBoxResponse
		if err := m.getJSON(ctx, seasonPath, &seasonResponse); err != nil {
			// Details remain usable even if season metadata is unavailable.
			subject["season_info_error"] = err.Error()
		} else {
			subject["seasons"] = unwrapData(seasonResponse)
		}
	}

	return result, nil
}

// Episodes implements MovieBoxService.Episodes.
// It exposes episode metadata; it does not claim that an episode has a playable stream.
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
		return nil, errors.New("invalid MovieBox details response")
	}

	subject := findSubject(payload)
	if subject == nil {
		return nil, errors.New("subject metadata unavailable")
	}

	seasons := extractSeasons(subject)
	if len(seasons) == 0 {
		// Some responses put the season payload at the root.
		seasons = extractSeasons(payload)
	}
	if len(seasons) == 0 {
		return nil, errors.New("MovieBox season information unavailable")
	}

	var selected map[string]any
	for _, item := range seasons {
		if readInt(item, "se", "season", "seasonNumber") == season {
			selected = item
			break
		}
	}

	// Season zero means first available season.
	if selected == nil && season == 0 {
		selected = seasons[0]
	}
	if selected == nil {
		return nil, fmt.Errorf("season %d not found", season)
	}

	seasonNumber := readInt(selected, "se", "season", "seasonNumber")
	if seasonNumber == 0 {
		seasonNumber = season
	}

	episodeNumbers := extractEpisodeNumbers(selected)
	episodes := make([]map[string]any, 0, len(episodeNumbers))

	for _, number := range episodeNumbers {
		episodes = append(episodes, map[string]any{
			"season":  seasonNumber,
			"episode": number,
			"title":   fmt.Sprintf("Episode %d", number),
		})
	}

	return map[string]any{
		"provider":   "moviebox",
		"subject_id": subjectID,
		"season":     seasonNumber,
		"episodes":   episodes,
		"season_data": selected,
	}, nil
}

// Sources implements MovieBoxService.Sources.
// URLs returned here are metadata candidates, not verified playable streams.
func (m *MovieBoxClient) Sources(
	ctx context.Context,
	subjectID string,
	season int,
	episode int,
) (any, error) {
	if err := validateID(subjectID); err != nil {
		return nil, err
	}
	if season < 0 || season > 1000 ||
		episode < 1 || episode > 100000 {
		return nil, errors.New("invalid season or episode")
	}

	query := url.Values{}
	query.Set("subjectId", subjectID)
	query.Set("se", strconv.Itoa(season))
	query.Set("ep", strconv.Itoa(episode))

	playPath := "/wefeed-mobile-bff/subject-api/play-info/v2?" + query.Encode()

	resourceQuery := url.Values{}
	resourceQuery.Set("subjectId", subjectID)
	resourceQuery.Set("se", strconv.Itoa(season))
	resourceQuery.Set("ep", strconv.Itoa(episode))
	resourceQuery.Set("page", "1")
	resourceQuery.Set("perPage", "50")

	resourcePath := "/wefeed-mobile-bff/subject-api/resource?" +
		resourceQuery.Encode()

	type result struct {
		data movieBoxResponse
		err  error
	}

	playCh := make(chan result, 1)
	resourceCh := make(chan result, 1)

	go func() {
		var data movieBoxResponse
		err := m.getJSON(ctx, playPath, &data)
		playCh <- result{data: data, err: err}
	}()

	go func() {
		var data movieBoxResponse
		err := m.getJSON(ctx, resourcePath, &data)
		resourceCh <- result{data: data, err: err}
	}()

	var play, resource result

	select {
	case play = <-playCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case resource = <-resourceCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if play.err != nil && resource.err != nil {
		return nil, fmt.Errorf(
			"MovieBox source metadata unavailable: play-info: %v; resource: %v",
			play.err,
			resource.err,
		)
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

	return map[string]any{
		"provider":   "moviebox",
		"subject_id": subjectID,
		"season":     season,
		"episode":    episode,
		"status":     "success",
		"available":  len(sources) > 0,
		"verified":   false,
		"sources":    sources,
	}, nil
}

// Subtitles implements MovieBoxService.Subtitles.
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

	query := url.Values{}
	query.Set("subjectId", subjectID)
	query.Set("resourceId", resourceID)

	path := "/wefeed-mobile-bff/subject-api/get-ext-captions?" +
		query.Encode()

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
	if ctx == nil {
		return errors.New("request context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return errors.New("invalid API path")
	}

	encodedBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	hosts, err := m.hosts()
	if err != nil {
		return err
	}

	var lastErr error

	for _, host := range hosts {
		if err := ctx.Err(); err != nil {
			return err
		}

		endpoint := strings.TrimRight(host, "/") + path

		var requestBody io.Reader
		if body != nil {
			requestBody = bytes.NewReader(encodedBody)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			method,
			endpoint,
			requestBody,
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
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			continue
		}

		data, readErr := io.ReadAll(
			io.LimitReader(resp.Body, maxResponseSize+1),
		)
		resp.Body.Close()

		if readErr != nil {
			lastErr = fmt.Errorf("read upstream response: %w", readErr)
			continue
		}

		if len(data) > maxResponseSize {
			return errors.New("MovieBox response exceeds size limit")
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			message := extractErrorMessage(data)
			httpErr := &movieBoxHTTPError{
				StatusCode: resp.StatusCode,
				Message:    message,
			}

			// A 401/403 is an authorization response.
			// Do not disguise it as a successful host failover.
			if resp.StatusCode == http.StatusUnauthorized ||
				resp.StatusCode == http.StatusForbidden {
				return httpErr
			}

			// Retry transient upstream failures on another configured host.
			if resp.StatusCode == http.StatusTooManyRequests ||
				resp.StatusCode >= 500 {
				lastErr = httpErr
				continue
			}

			return httpErr
		}

		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode MovieBox JSON: %w", err)
		}

		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("no MovieBox API host available")
	}

	return lastErr
}

func (m *MovieBoxClient) hosts() ([]string, error) {
	raw := strings.TrimSpace(m.baseURL)
	if raw == "" {
		raw = defaultMovieBoxHost
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" ||
		(u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid MOVIEBOX_API_BASE_URL")
	}

	if raw == defaultMovieBoxHost {
		return movieBoxHosts, nil
	}

	return []string{strings.TrimRight(raw, "/")}, nil
}

// Response adapters.

func unwrapData(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}

	if nested, ok := data["data"].(map[string]any); ok {
		return nested
	}

	return data
}

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

	if items := toMaps(value); len(items) > 0 {
		return items
	}

	// Some season-info responses return data.seasons.
	if nested, ok := data["data"].(map[string]any); ok {
		value = nested["seasons"]
		if inner, ok := value.(map[string]any); ok {
			value = inner["seasons"]
		}
		return toMaps(value)
	}

	return nil
}

func extractEpisodeNumbers(season map[string]any) []int {
	if season == nil {
		return nil
	}

	value := season["episodeNumbers"]
	if value == nil {
		value = season["episodes"]
	}

	result := make([]int, 0)

	switch v := value.(type) {
	case []any:
		for _, item := range v {
			switch n := item.(type) {
			case float64:
				if n > 0 {
					result = append(result, int(n))
				}
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && parsed > 0 {
					result = append(result, parsed)
				}
			case map[string]any:
				number := readInt(n, "ep", "episode", "number", "episodeNumber")
				if number > 0 {
					result = append(result, number)
				}
			}
		}
	}

	if len(result) == 0 {
		max := readInt(season, "maxEp", "episodeCount", "totalEpisodes")
		if max > 0 && max <= 10000 {
			for i := 1; i <= max; i++ {
				result = append(result, i)
			}
		}
	}

	return result
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
		if depth > 6 {
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
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return false
	}

	host := strings.ToLower(u.Hostname())

	if host == "localhost" || host == "127.0.0.1" ||
		host == "::1" || strings.HasSuffix(host, ".local") {
		return false
	}

	lower := strings.ToLower(raw)
	for _, word := range []string{
		"notice", "deprecation", "placeholder", "error.mp4",
	} {
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

	u, err := url.Parse(raw)
	if err != nil {
		return
	}

	key := u.Host + u.Path
	if seen[key] {
		return
	}

	seen[key] = true
	*sources = append(*sources, source)
}

func extractCaptions(data map[string]any) []map[string]any {
	if data == nil {
		return []map[string]any{}
	}

	if nested, ok := data["data"].(map[string]any); ok {
		data = nested
	}

	items := toMaps(data["extCaptions"])
	if len(items) == 0 {
		items = toMaps(data["captions"])
	}

	result := make([]map[string]any, 0, len(items))

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
			n, _ := value.Int64()
			return int(n)
		case string:
			n, _ := strconv.Atoi(strings.TrimSpace(value))
			if n != 0 {
				return n
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

func extractErrorMessage(data []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}

	for _, key := range []string{"message", "msg", "error", "detail"} {
		if value, ok := payload[key].(string); ok {
			value = strings.TrimSpace(value)
			if len(value) > 300 {
				value = value[:300]
			}
			return value
		}
	}

	return ""
}

var _ MovieBoxService = (*MovieBoxClient)(nil)
