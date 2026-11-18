
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMovieBoxHost = "https://api6.aoneroom.com"
	defaultTimeout      = 25 * time.Second
	maxResponseSize     = 8 << 20
	sessionSafetyWindow = 60 * time.Second
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

	sessionMu sync.Mutex
	token     string
	expiresAt time.Time

	sessionFile string
}

type movieBoxHTTPError struct {
	StatusCode int
	Message    string
}

type movieBoxSession struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (e *movieBoxHTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("MovieBox HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("MovieBox HTTP %d: %s", e.StatusCode, e.Message)
}

// --------------------------------------------------
// CLIENT
// --------------------------------------------------

func NewMovieBoxClient() *MovieBoxClient {
	timeout := defaultTimeout

	for _, key := range []string{
		"MOVIEBOX_REQUEST_TIMEOUT",
		"REQUEST_TIMEOUT",
	} {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}

		seconds, err := strconv.Atoi(value)
		if err == nil && seconds >= 1 && seconds <= 120 {
			timeout = time.Duration(seconds) * time.Second
			break
		}
	}

	host := strings.TrimRight(
		strings.TrimSpace(os.Getenv("MOVIEBOX_API_BASE_URL")),
		"/",
	)
	if host == "" {
		host = defaultMovieBoxHost
	}

	client := &MovieBoxClient{
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
		baseURL:     host,
		sessionFile: strings.TrimSpace(os.Getenv("MOVIEBOX_SESSION_FILE")),
	}

	// An operator-provided token takes precedence.
	client.token = strings.TrimSpace(os.Getenv("MOVIEBOX_API_TOKEN"))
	if client.token != "" {
		client.expiresAt = tokenExpiry(client.token)
	}

	// Load a persisted visitor session only if no operator token exists.
	if client.token == "" {
		client.loadSession()
	}

	return client
}

// --------------------------------------------------
// SESSION MANAGEMENT
// --------------------------------------------------

func (m *MovieBoxClient) ensureSession(
	ctx context.Context,
) (string, error) {
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()

	if m.token != "" && !m.sessionExpiredLocked() {
		return m.token, nil
	}

	// Do not silently replace an explicitly configured token.
	operatorToken := strings.TrimSpace(os.Getenv("MOVIEBOX_API_TOKEN"))
	if operatorToken != "" {
		m.token = operatorToken
		m.expiresAt = tokenExpiry(operatorToken)

		if m.expiresAt.IsZero() ||
			time.Now().Add(sessionSafetyWindow).Before(m.expiresAt) {
			return m.token, nil
		}
	}

	// Obtain a visitor session through the documented endpoint.
	var response map[string]any

	err := m.requestRaw(
		ctx,
		http.MethodPost,
		"/wefeed-mobile-bff/user-api/visitor-login",
		map[string]any{},
		"",
		&response,
	)
	if err != nil {
		return "", fmt.Errorf("MovieBox visitor login failed: %w", err)
	}

	data := unwrapData(response)
	token := readString(
		data,
		"token",
		"accessToken",
		"access_token",
	)

	if token == "" {
		return "", errors.New(
			"MovieBox visitor login returned no access token",
		)
	}

	m.token = token
	m.expiresAt = tokenExpiry(token)

	if err := m.saveSessionLocked(); err != nil {
		// The session can still be used in memory.
		// Persistence failure must not hide a successful login.
		fmt.Printf("MovieBox session persistence warning: %v\n", err)
	}

	return m.token, nil
}

func (m *MovieBoxClient) sessionExpiredLocked() bool {
	if m.expiresAt.IsZero() {
		return false
	}

	return !time.Now().Add(sessionSafetyWindow).Before(m.expiresAt)
}

func (m *MovieBoxClient) clearSessionIfMatches(token string) {
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()

	if m.token != token {
		return
	}

	m.token = ""
	m.expiresAt = time.Time{}

	if m.sessionFile != "" {
		if err := os.Remove(m.sessionFile); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			fmt.Printf("MovieBox session cleanup warning: %v\n", err)
		}
	}
}

func (m *MovieBoxClient) loadSession() {
	if m.sessionFile == "" {
		return
	}

	data, err := os.ReadFile(m.sessionFile)
	if err != nil {
		return
	}

	var session movieBoxSession
	if err := json.Unmarshal(data, &session); err != nil {
		return
	}

	if strings.TrimSpace(session.Token) == "" {
		return
	}

	if !session.ExpiresAt.IsZero() &&
		time.Now().Add(sessionSafetyWindow).After(session.ExpiresAt) {
		return
	}

	m.token = session.Token
	m.expiresAt = session.ExpiresAt
}

func (m *MovieBoxClient) saveSessionLocked() error {
	if m.sessionFile == "" {
		return nil
	}

	if strings.TrimSpace(m.token) == "" {
		return errors.New("cannot persist an empty session")
	}

	session := movieBoxSession{
		Token:     m.token,
		ExpiresAt: m.expiresAt,
		UpdatedAt: time.Now().UTC(),
	}

	data, err := json.Marshal(session)
	if err != nil {
		return err
	}

	dir := filepath.Dir(m.sessionFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	tmp := m.sessionFile + ".tmp"

	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}

	if err := os.Rename(tmp, m.sessionFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	return nil
}

func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}

	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return time.Time{}
	}

	exp := readInt(claims, "exp")
	if exp <= 0 {
		return time.Time{}
	}

	return time.Unix(int64(exp), 0)
}

// --------------------------------------------------
// SEARCH
// --------------------------------------------------

func (m *MovieBoxClient) Search(
	ctx context.Context,
	query string,
	page int,
) (any, error) {
	query = strings.TrimSpace(query)

	if query == "" || len(query) > 200 {
		return nil, errors.New("invalid search query")
	}

	if page < 1 || page > 10000 {
		return nil, errors.New("invalid page")
	}

	payload := map[string]any{
		"keyword":     query,
		"page":        page,
		"perPage":     15,
		"subjectType": 0,
	}

	var result map[string]any

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

// --------------------------------------------------
// DETAILS
// --------------------------------------------------

func (m *MovieBoxClient) Details(
	ctx context.Context,
	subjectID string,
) (any, error) {
	if err := validateID(subjectID); err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("subjectId", subjectID)

	path := "/wefeed-mobile-bff/subject-api/get?" + query.Encode()

	var result map[string]any

	if err := m.getJSON(ctx, path, &result); err != nil {
		return nil, err
	}

	subject := findSubject(result)
	if subject == nil {
		return nil, errors.New("MovieBox returned no subject details")
	}

	subjectType := readInt(subject, "subjectType", "stype")

	if subjectType == 2 {
		seasonQuery := url.Values{}
		seasonQuery.Set("subjectId", subjectID)

		seasonPath := "/wefeed-mobile-bff/subject-api/season-info?" +
			seasonQuery.Encode()

		var seasons map[string]any

		if err := m.getJSON(ctx, seasonPath, &seasons); err != nil {
			return nil, fmt.Errorf("MovieBox season information: %w", err)
		}

		subject["seasons"] = unwrapData(seasons)
	}

	return result, nil
}

// --------------------------------------------------
// EPISODES
// --------------------------------------------------

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
		return nil, errors.New("subject metadata unavailable")
	}

	seasons := extractSeasons(subject)
	if len(seasons) == 0 {
		seasons = extractSeasons(payload)
	}

	if len(seasons) == 0 {
		return nil, errors.New("MovieBox season information unavailable")
	}

	var selected map[string]any

	for _, item := range seasons {
		number := readInt(item, "se", "season", "seasonNumber")
		if number == season {
			selected = item
			break
		}
	}

	if selected == nil && season == 0 {
		selected = seasons[0]
	}

	if selected == nil {
		return nil, fmt.Errorf("season %d not found", season)
	}

	seasonNumber := readInt(
		selected,
		"se",
		"season",
		"seasonNumber",
	)
	if seasonNumber == 0 {
		seasonNumber = season
	}

	numbers := extractEpisodeNumbers(selected)
	episodes := make([]map[string]any, 0, len(numbers))

	for _, number := range numbers {
		episodes = append(episodes, map[string]any{
			"season":  seasonNumber,
			"episode": number,
			"title":   fmt.Sprintf("Episode %d", number),
		})
	}

	return map[string]any{
		"provider":      "moviebox",
		"subject_id":    subjectID,
		"season":        seasonNumber,
		"episode_count": len(episodes),
		"episodes":      episodes,
		"season_data":   selected,
	}, nil
}

// --------------------------------------------------
// SOURCES
// --------------------------------------------------

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

	playQuery := url.Values{}
	playQuery.Set("subjectId", subjectID)
	playQuery.Set("se", strconv.Itoa(season))
	playQuery.Set("ep", strconv.Itoa(episode))

	playPath := "/wefeed-mobile-bff/subject-api/play-info/v2?" +
		playQuery.Encode()

	resourceQuery := url.Values{}
	resourceQuery.Set("subjectId", subjectID)
	resourceQuery.Set("page", "1")
	resourceQuery.Set("perPage", "20")
	resourceQuery.Set("all", "0")
	resourceQuery.Set("startPosition", "1")
	resourceQuery.Set("endPosition", "1")
	resourceQuery.Set("pagerMode", "2")
	resourceQuery.Set("resolution", "0")
	resourceQuery.Set("se", strconv.Itoa(season))
	resourceQuery.Set("epFrom", strconv.Itoa(episode))
	resourceQuery.Set("epTo", strconv.Itoa(episode))
	resourceQuery.Set("isVip", "true")

	resourcePath := "/wefeed-mobile-bff/subject-api/resource/v2?" +
		resourceQuery.Encode()

	type result struct {
		data map[string]any
		err  error
	}

	playCh := make(chan result, 1)
	resourceCh := make(chan result, 1)

	go func() {
		var data map[string]any
		err := m.getJSON(ctx, playPath, &data)
		playCh <- result{data: data, err: err}
	}()

	go func() {
		var data map[string]any
		err := m.getJSON(ctx, resourcePath, &data)
		resourceCh <- result{data: data, err: err}
	}()

	var play, resources result

	select {
	case play = <-playCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case resources = <-resourceCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if play.err != nil && resources.err != nil {
		return nil, fmt.Errorf(
			"source metadata unavailable: play-info: %v; resource: %v",
			play.err,
			resources.err,
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

	if resources.err == nil {
		for _, item := range extractResourceItems(resources.data) {
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

// --------------------------------------------------
// SUBTITLES
// --------------------------------------------------

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

	var result map[string]any

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

// --------------------------------------------------
// HTTP REQUEST LAYER
// --------------------------------------------------

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
	token, err := m.ensureSession(ctx)
	if err != nil {
		return err
	}

	err = m.requestRaw(ctx, method, path, body, token, out)

	var upstream *movieBoxHTTPError
	if !errors.As(err, &upstream) ||
		upstream.StatusCode != http.StatusUnauthorized {
		return err
	}

	// Refresh once after HTTP 401.
	m.clearSessionIfMatches(token)

	newToken, sessionErr := m.ensureSession(ctx)
	if sessionErr != nil {
		return fmt.Errorf(
			"session refresh after HTTP 401 failed: %w",
			sessionErr,
		)
	}

	return m.requestRaw(ctx, method, path, body, newToken, out)
}

func (m *MovieBoxClient) requestRaw(
	ctx context.Context,
	method string,
	path string,
	body any,
	token string,
	out any,
) error {
	if ctx == nil {
		return errors.New("request context is required")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if !strings.HasPrefix(path, "/") ||
		strings.HasPrefix(path, "//") {
		return errors.New("invalid API path")
	}

	hosts, err := m.hosts()
	if err != nil {
		return err
	}

	var encoded []byte

	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}

	var lastErr error

	for _, host := range hosts {
		if err := ctx.Err(); err != nil {
			return err
		}

		endpoint := strings.TrimRight(host, "/") + path

		var requestBody io.Reader
		if body != nil {
			requestBody = bytes.NewReader(encoded)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			method,
			endpoint,
			requestBody,
		)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")

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
			lastErr = readErr
			continue
		}

		if len(data) > maxResponseSize {
			return errors.New("MovieBox response exceeds size limit")
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			httpErr := &movieBoxHTTPError{
				StatusCode: resp.StatusCode,
				Message:    extractErrorMessage(data),
			}

			// Do not rotate hosts or bypass authentication failures.
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

		if out == nil {
			return nil
		}

		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()

		if err := decoder.Decode(out); err != nil {
			return fmt.Errorf("decode MovieBox response: %w", err)
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
	if err != nil ||
		u.Host == "" ||
		(u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" {
		return nil, errors.New("invalid MOVIEBOX_API_BASE_URL")
	}

	raw = strings.TrimRight(raw, "/")

	if raw == defaultMovieBoxHost {
		return movieBoxHosts, nil
	}

	return []string{raw}, nil
}

// --------------------------------------------------
// RESPONSE HELPERS
// --------------------------------------------------

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

// --------------------------------------------------
// SEASON / EPISODE PARSERS
// --------------------------------------------------

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

	switch items := value.(type) {
	case []any:
		for _, item := range items {
			switch current := item.(type) {
			case json.Number:
				number, _ := current.Int64()
				if number > 0 {
					result = append(result, int(number))
				}
			case float64:
				if current > 0 {
					result = append(result, int(current))
				}
			case int:
				if current > 0 {
					result = append(result, current)
				}
			case string:
				if number, err := strconv.Atoi(strings.TrimSpace(current)); err == nil && number > 0 {
					result = append(result, number)
				}
			case map[string]any:
				number := readInt(
					current,
					"ep",
					"episode",
					"number",
					"episodeNumber",
				)
				if number > 0 {
					result = append(result, number)
				}
			}
		}
	}

	if len(result) == 0 {
		max := readInt(
			season,
			"maxEp",
			"episodeCount",
			"totalEpisodes",
		)

		if max > 0 && max <= 10000 {
			for i := 1; i <= max; i++ {
				result = append(result, i)
			}
		}
	}

	return result
}

// --------------------------------------------------
// RESOURCE PARSERS
// --------------------------------------------------

func extractResourceItems(data map[string]any) []map[string]any {
	if data == nil {
		return nil
	}

	if nested, ok := data["data"].(map[string]any); ok {
		data = nested
	}

	items := toMaps(data["list"])
	if len(items) > 0 {
		return items
	}

	return toMaps(data["resources"])
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
				case "streams",
					"list",
					"data",
					"resources",
					"resourceList",
					"playList",
					"sources":
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
		"verified": false,
	}
}

func validMediaURL(raw string) bool {
	raw = strings.TrimSpace(raw)

	if raw == "" || len(raw) > 8192 {
		return false
	}

	u, err := url.Parse(raw)
	if err != nil ||
		u.Scheme != "https" ||
		u.Hostname() == "" ||
		u.User != nil {
		return false
	}

	host := strings.ToLower(u.Hostname())

	if host == "localhost" ||
		host == "127.0.0.1" ||
		host == "::1" ||
		strings.HasSuffix(host, ".local") {
		return false
	}

	lower := strings.ToLower(raw)

	for _, word := range []string{
		"notice",
		"deprecation",
		"placeholder",
		"error.mp4",
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

	key := strings.ToLower(u.Host) + u.EscapedPath()
	if seen[key] {
		return
	}

	seen[key] = true
	*sources = append(*sources, source)
}

// --------------------------------------------------
// SUBTITLE PARSER
// --------------------------------------------------

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
			"id":       readString(item, "id", "resourceId"),
			"language": readString(item, "lanName", "lan", "language"),
			"url":      rawURL,
			"size":     readString(item, "size"),
			"format":   readString(item, "format", "type"),
		})
	}

	return result
}

// --------------------------------------------------
// GENERIC HELPERS
// --------------------------------------------------

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
	if data == nil {
		return ""
	}

	for _, key := range keys {
		switch value := data[key].(type) {
		case string:
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		case json.Number:
			return value.String()
		case float64:
			return strconv.FormatFloat(value, 'f', -1, 64)
		case int:
			return strconv.Itoa(value)
		}
	}

	return ""
}

func readInt(data map[string]any, keys ...string) int {
	if data == nil {
		return 0
	}

	for _, key := range keys {
		switch value := data[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case int64:
			return int(value)
		case json.Number:
			number, _ := value.Int64()
			return int(number)
		case string:
			number, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil {
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

func extractErrorMessage(data []byte) string {
	var payload map[string]any

	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}

	for _, key := range []string{
		"message",
		"msg",
		"error",
		"detail",
	} {
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

// Compile-time interface verification.
var _ MovieBoxService = (*MovieBoxClient)(nil)
