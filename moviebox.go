
package main

import (
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

const defaultMovieBoxBaseURL = "https://api.inmoviebox.com"

type MovieBoxClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

func NewMovieBoxClient() *MovieBoxClient {
	baseURL := strings.TrimRight(
		getenv("MOVIEBOX_API_BASE_URL"),
		"/",
	)

	if baseURL == "" {
		baseURL = defaultMovieBoxBaseURL
	}

	return &MovieBoxClient{
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		baseURL: baseURL,
		token:   strings.TrimSpace(getenv("MOVIEBOX_API_TOKEN")),
	}
}

func getenv(key string) string {
	return os.Getenv(key)
}

// Search uses MovieBox subject-api/search/v2.
func (m *MovieBoxClient) Search(
	ctx context.Context,
	query string,
	page int,
) (any, error) {
	if strings.TrimSpace(query) == "" {
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
		"/wefeed-mobile-bff/subject-api/search/v2",
		payload,
	)
}

// Details uses MovieBox subject-api/get.
// Series details also include season-info.
func (m *MovieBoxClient) Details(
	ctx context.Context,
	subjectID string,
) (any, error) {
	if strings.TrimSpace(subjectID) == "" {
		return nil, errors.New("subject ID is required")
	}

	path := "/wefeed-mobile-bff/subject-api/get"

	details, err := m.getJSON(ctx, path, url.Values{
		"subjectId": []string{subjectID},
	})
	if err != nil {
		return nil, err
	}

	stype := readInt(details, "subjectType")
	if stype == 0 {
		stype = readInt(details, "stype")
	}

	// MovieBox series type.
	if stype == 2 {
		seasons, seasonErr := m.getJSON(
			ctx,
			"/wefeed-mobile-bff/subject-api/season-info",
			url.Values{
				"subjectId": []string{subjectID},
			},
		)

		if seasonErr == nil {
			details["seasons"] = seasons
		}
	}

	return details, nil
}

// Episodes reads the resource list for a selected season.
func (m *MovieBoxClient) Episodes(
	ctx context.Context,
	subjectID string,
	season int,
) (any, error) {
	if strings.TrimSpace(subjectID) == "" {
		return nil, errors.New("subject ID is required")
	}

	if season < 0 {
		return nil, errors.New("invalid season")
	}

	query := url.Values{
		"subjectId": []string{subjectID},
		"page":      []string{"1"},
		"perPage":   []string{"20"},
	}

	if season > 0 {
		query.Set("se", strconv.Itoa(season))
	}

	return m.getJSON(
		ctx,
		"/wefeed-mobile-bff/subject-api/resource",
		query,
	)
}

// Sources combines playback information and resource metadata.
func (m *MovieBoxClient) Sources(
	ctx context.Context,
	subjectID string,
	season int,
	episode int,
) (any, error) {
	if strings.TrimSpace(subjectID) == "" {
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

	if season > 0 && episode > 0 {
		playQuery.Set("se", strconv.Itoa(season))
		playQuery.Set("ep", strconv.Itoa(episode))

		resourceQuery.Set("se", strconv.Itoa(season))
		resourceQuery.Set("ep", strconv.Itoa(episode))
	}

	type result struct {
		data any
		err  error
	}

	playChannel := make(chan result, 1)
	resourceChannel := make(chan result, 1)

	go func() {
		data, err := m.getJSON(
			ctx,
			"/wefeed-mobile-bff/subject-api/play-info/v2",
			playQuery,
		)
		playChannel <- result{data: data, err: err}
	}()

	go func() {
		data, err := m.getJSON(
			ctx,
			"/wefeed-mobile-bff/subject-api/resource",
			resourceQuery,
		)
		resourceChannel <- result{data: data, err: err}
	}()

	playResult := <-playChannel
	resourceResult := <-resourceChannel

	if playResult.err != nil && resourceResult.err != nil {
		return nil, fmt.Errorf(
			"play-info request failed: %v; resource request failed: %v",
			playResult.err,
			resourceResult.err,
		)
	}

	return map[string]any{
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

// Subtitles uses MovieBox subject-api/get-ext-captions.
func (m *MovieBoxClient) Subtitles(
	ctx context.Context,
	subjectID string,
	resourceID string,
) (any, error) {
	if strings.TrimSpace(subjectID) == "" {
		return nil, errors.New("subject ID is required")
	}

	if strings.TrimSpace(resourceID) == "" {
		return nil, errors.New("resource ID is required")
	}

	return m.getJSON(
		ctx,
		"/wefeed-mobile-bff/subject-api/get-ext-captions",
		url.Values{
			"subjectId":  []string{subjectID},
			"resourceId": []string{resourceID},
		},
	)
}

func (m *MovieBoxClient) getJSON(
	ctx context.Context,
	path string,
	query url.Values,
) (map[string]any, error) {
	target := m.baseURL + path

	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	return m.doJSON(ctx, http.MethodGet, target, nil)
}

func (m *MovieBoxClient) postJSON(
	ctx context.Context,
	path string,
	payload any,
) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	return m.doJSON(
		ctx,
		http.MethodPost,
		m.baseURL+path,
		body,
	)
}

func (m *MovieBoxClient) doJSON(
	ctx context.Context,
	method string,
	target string,
	body []byte,
) (map[string]any, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		method,
		target,
		strings.NewReader(string(body)),
	)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	// Use an authorized API token if one has been configured.
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("MovieBox request: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseSize = 10 << 20

	responseBody, err := io.ReadAll(
		io.LimitReader(resp.Body, maxResponseSize),
	)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf(
			"MovieBox returned HTTP %d",
			resp.StatusCode,
		)
	}

	var result map[string]any

	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("decode MovieBox JSON: %w", err)
	}

	return result, nil
}

func readInt(data map[string]any, key string) int {
	value, ok := data[key]
	if !ok {
		return 0
	}

	switch v := value.(type) {
	case float64:
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

var _ MovieBoxService = (*MovieBoxClient)(nil)
