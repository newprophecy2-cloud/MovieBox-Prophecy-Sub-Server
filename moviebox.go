
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	movieBoxAPIPath = "/wefeed-mobile-bff"
	maxResponseSize = 10 << 20
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
}

func NewMovieBoxClient() *MovieBoxClient {
	baseURL := strings.TrimRight(
		strings.TrimSpace(os.Getenv("MOVIEBOX_API_BASE_URL")),
		"/",
	)

	if baseURL == "" {
		baseURL = movieBoxHosts[0]
	}

	return &MovieBoxClient{
		httpClient: &http.Client{
			Timeout: 25 * time.Second,
		},
		baseURL: baseURL,
		token:   strings.TrimSpace(os.Getenv("MOVIEBOX_API_TOKEN")),
	}
}

// Search
// POST /subject-api/search/v2
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

	return m.postJSON(
		ctx,
		"/subject-api/search/v2",
		payload,
	)
}

// Details
// GET /subject-api/get
// Series also requests /subject-api/season-info.
func (m *MovieBoxClient) Details(
	ctx context.Context,
	subjectID string,
) (any, error) {
	subjectID = strings.TrimSpace(subjectID)

	if subjectID == "" {
		return nil, errors.New("subject ID is required")
	}

	details, err := m.getJSON(
		ctx,
		"/subject-api/get",
		url.Values{
			"subjectId": []string{subjectID},
		},
	)
	if err != nil {
		return nil, err
	}

	subject := findSubject(details)

	if subject == nil {
		return details, nil
	}

	stype := readInt(subject, "subjectType")
	if stype == 0 {
		stype = readInt(subject, "stype")
	}

	if stype == 2 {
		seasons, seasonErr := m.getJSON(
			ctx,
			"/subject-api/season-info",
			url.Values{
				"subjectId": []string{subjectID},
			},
		)

		if seasonErr != nil {
			return nil, fmt.Errorf(
				"season info request failed: %w",
				seasonErr,
			)
		}

		subject["seasons"] = seasons
	}

	return details, nil
}

// Episodes
// MovieBox exposes episode numbers through details/season-info.
// It does not use a separate episode-list endpoint.
func (m *MovieBoxClient) Episodes(
	ctx context.Context,
	subjectID string,
	season int,
) (any, error) {
	if season < 0 {
		return nil, errors.New("season cannot be negative")
	}

	details, err := m.Details(ctx, subjectID)
	if err != nil {
		return nil, err
	}

	root, ok := details.(map[string]any)
	if !ok {
		return nil, errors.New("invalid details response")
	}

	subject := findSubject(root)
	if subject == nil {
		return nil, errors.New("subject data not found")
	}

	seasons := extractSeasons(subject)

	if len(seasons) == 0 {
		return map[string]any{
			"subject_id": subjectID,
			"episodes":   []any{},
		}, nil
	}

	result := make([]map[string]any, 0)

	for _, s := range seasons {
		seasonNumber := readInt(s, "se")

		if seasonNumber == 0 {
			seasonNumber = 1
		}

		if season > 0 && seasonNumber != season {
			continue
		}

		episodes := extractEpisodeNumbers(s)

		result = append(result, map[string]any{
			"season":   seasonNumber,
			"episodes": episodes,
		})
	}

	return map[string]any{
		"subject_id": subjectID,
		"seasons":    result,
	}, nil
}

// Sources
// GET /subject-api/play-info/v2
// GET /subject-api/resource
func (m *MovieBoxClient) Sources(
	ctx context.Context,
	subjectID string,
	season int,
	episode int,
) (any, error) {
	subjectID = strings.TrimSpace(subjectID)

	if subjectID == "" {
		return nil, errors.New("subject ID is required")
	}

	if season < 0 || episode < 0 {
		return nil, errors.New("invalid season or episode")
	}

	if (season == 0) != (episode == 0) {
		return nil, errors.New(
			"season and episode must both be zero or positive",
		)
	}

	playQuery := url.Values{
		"subjectId": []string{subjectID},
	}

	resourceQuery := url.Values{
		"subjectId": []string{subjectID},
		"page":      []string{"1"},
		"perPage":   []string{"20"},
	}

	if season > 0 {
		playQuery.Set("se", strconv.Itoa(season))
		playQuery.Set("ep", strconv.Itoa(episode))

		resourceQuery.Set("se", strconv.Itoa(season))
		resourceQuery.Set("ep", strconv.Itoa(episode))
	}

	type response struct {
		data map[string]any
		err  error
	}

	playCh := make(chan response, 1)
	resourceCh := make(chan response, 1)

	go func() {
		data, err := m.getJSON(
			ctx,
			"/subject-api/play-info/v2",
			playQuery,
		)
		playCh <- response{data: data, err: err}
	}()

	go func() {
		data, err := m.getJSON(
			ctx,
			"/subject-api/resource",
			resourceQuery,
		)
		resourceCh <- response{data: data, err: err}
	}()

	playResult := <-playCh
	resourceResult := <-resourceCh

	if playResult.err != nil && resourceResult.err != nil {
		return nil, fmt.Errorf(
			"play-info: %v; resources: %v",
			playResult.err,
			resourceResult.err,
		)
	}

	return map[string]any{
		"provider":   "moviebox",
		"subject_id": subjectID,
		"season":     season,
		"episode":    episode,
		"play_info":  playResult.data,
		"resources":  resourceResult.data,
		"errors": map[string]any{
			"play_info": errorString(playResult.err),
			"resources": errorString(resourceResult.err),
		},
	}, nil
}

// Subtitles
// GET /subject-api/get-ext-captions
func (m *MovieBoxClient) Subtitles(
	ctx context.Context,
	subjectID string,
	resourceID string,
) (any, error) {
	subjectID = strings.TrimSpace(subjectID)
	resourceID = strings.TrimSpace(resourceID)

	if subjectID == "" {
		return nil, errors.New("subject ID is required")
	}

	if resourceID == "" {
		return nil, errors.New("resource ID is required")
	}

	return m.getJSON(
		ctx,
		"/subject-api/get-ext-captions",
		url.Values{
			"subjectId":  []string{subjectID},
			"resourceId": []string{resourceID},
		},
	)
}

// HTTP GET
func (m *MovieBoxClient) getJSON(
	ctx context.Context,
	path string,
	query url.Values,
) (map[string]any, error) {
	target := m.baseURL +
		movieBoxAPIPath +
		path

	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	return m.doJSON(ctx, http.MethodGet, target, nil)
}

// HTTP POST
func (m *MovieBoxClient) postJSON(
	ctx context.Context,
	path string,
	payload any,
) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	target := m.baseURL +
		movieBoxAPIPath +
		path

	return m.doJSON(
		ctx,
		http.MethodPost,
		target,
		body,
	)
}

// Shared HTTP client
func (m *MovieBoxClient) doJSON(
	ctx context.Context,
	method string,
	target string,
	body []byte,
) (map[string]any, error) {
	var requestBody io.Reader

	if body != nil {
		requestBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		method,
		target,
		requestBody,
	)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	// Only use a token supplied through authorized configuration.
	if m.token != "" {
		req.Header.Set(
			"Authorization",
			"Bearer "+m.token,
		)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("MovieBox HTTP request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(
		io.LimitReader(resp.Body, maxResponseSize),
	)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf(
			"MovieBox HTTP %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(responseBody)),
		)
	}

	var result map[string]any

	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf(
			"decode MovieBox response: %w",
			err,
		)
	}

	return result, nil
}

// Find subject in MovieBox response.
func findSubject(
	payload map[string]any,
) map[string]any {
	if data, ok := payload["data"].(map[string]any); ok {
		if subject, ok := data["subject"].(map[string]any); ok {
			return subject
		}
	}

	if subject, ok := payload["subject"].(map[string]any); ok {
		return subject
	}

	return payload
}

// Extract seasons from official response structure.
func extractSeasons(
	subject map[string]any,
) []map[string]any {
	raw := subject["seasons"]

	if wrapper, ok := raw.(map[string]any); ok {
		raw = wrapper["seasons"]
	}

	items, ok := raw.([]any)
	if !ok {
		return nil
	}

	result := make([]map[string]any, 0, len(items))

	for _, item := range items {
		if season, ok := item.(map[string]any); ok {
			result = append(result, season)
		}
	}

	return result
}

// Extract episode numbers from episodeNumbers or maxEp.
func extractEpisodeNumbers(
	season map[string]any,
) []int {
	result := make([]int, 0)

	if items, ok := season["episodeNumbers"].([]any); ok {
		for _, item := range items {
			switch v := item.(type) {
			case float64:
				if v > 0 {
					result = append(result, int(v))
				}
			case string:
				n, err := strconv.Atoi(v)
				if err == nil && n > 0 {
					result = append(result, n)
				}
			}
		}

		return result
	}

	maxEpisode := readInt(season, "maxEp")

	for i := 1; i <= maxEpisode && i <= 10000; i++ {
		result = append(result, i)
	}

	return result
}

func readInt(
	data map[string]any,
	key string,
) int {
	value, exists := data[key]
	if !exists {
		return 0
	}

	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

func errorString(err error) any {
	if err == nil {
		return nil
	}

	return err.Error()
}

// Compile-time interface check.
var _ MovieBoxService = (*MovieBoxClient)(nil)
