
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ParseMovieBoxSubject extracts a subject from a MovieBox JSON response.
func ParseMovieBoxSubject(raw json.RawMessage) (*MovieBoxSubject, error) {
	var root any

	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode subject: %w", err)
	}

	item := findSubject(root)
	if item == nil {
		return nil, fmt.Errorf("subject not found")
	}

	subject := &MovieBoxSubject{
		ID:          readString(item, "subjectId", "subject_id", "id"),
		Title:       readString(item, "title", "name", "subjectName"),
		Year:        readString(item, "year", "releaseYear"),
		Poster:      readString(item, "cover", "poster", "coverUrl"),
		Description: readString(item, "description", "introduction", "plot"),
		Rating:      readString(item, "imdbRating", "rating", "score"),
		Type:        readInt(item, "subjectType", "subject_type", "type"),
	}

	if subject.ID == "" {
		return nil, fmt.Errorf("subject ID missing")
	}

	if subject.Title == "" {
		subject.Title = subject.ID
	}

	return subject, nil
}

// ParseMovieBoxSeasons extracts season information.
func ParseMovieBoxSeasons(raw json.RawMessage) ([]MovieBoxSeason, error) {
	var root any

	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode seasons: %w", err)
	}

	items := collectMaps(root)
	seasons := make([]MovieBoxSeason, 0, len(items))
	seen := make(map[int]bool)

	for _, item := range items {
		number := readInt(item, "se", "season", "seasonNumber", "number")
		if number <= 0 || seen[number] {
			continue
		}

		seen[number] = true

		seasons = append(seasons, MovieBoxSeason{
			Number:       number,
			EpisodeCount: readInt(item, "maxEp", "episodeCount", "episode_count"),
			Language:     readString(item, "language", "lang"),
			SubjectID:    readString(item, "subjectId", "subject_id"),
		})
	}

	return seasons, nil
}

// ParseMovieBoxEpisodes extracts episodes for a given season.
func ParseMovieBoxEpisodes(raw json.RawMessage, season int) ([]MovieBoxEpisode, error) {
	if season <= 0 {
		return nil, fmt.Errorf("invalid season")
	}

	var root any

	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode episodes: %w", err)
	}

	items := collectMaps(root)
	episodes := make([]MovieBoxEpisode, 0, len(items))
	seen := make(map[int]bool)

	for _, item := range items {
		episodeNumber := readInt(
			item,
			"ep",
			"episode",
			"episodeNumber",
			"number",
		)

		if episodeNumber <= 0 || seen[episodeNumber] {
			continue
		}

		itemSeason := readInt(item, "se", "season", "seasonNumber")
		if itemSeason > 0 && itemSeason != season {
			continue
		}

		seen[episodeNumber] = true

		episodes = append(episodes, MovieBoxEpisode{
			Season:  season,
			Episode: episodeNumber,
			Title: readString(
				item,
				"title",
				"name",
				"episodeName",
			),
			Poster: readString(
				item,
				"cover",
				"poster",
				"coverUrl",
			),
		})
	}

	return episodes, nil
}

// ParseMovieBoxSources extracts playback resource metadata.
// This parser does not verify whether a stream is playable.
func ParseMovieBoxSources(raw json.RawMessage) ([]MovieBoxSource, error) {
	var root any

	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode sources: %w", err)
	}

	items := collectMaps(root)
	sources := make([]MovieBoxSource, 0, len(items))
	seen := make(map[string]bool)

	for _, item := range items {
		url := readString(
			item,
			"url",
			"playUrl",
			"resourceUrl",
			"streamUrl",
		)

		if url == "" {
			continue
		}

		source := MovieBoxSource{
			ID: readString(item, "id", "resourceId"),
			URL: url,
			Quality: readString(
				item,
				"quality",
				"resolution",
				"definition",
			),
			Codec: readString(item, "codec", "format"),
			Language: readString(
				item,
				"language",
				"lang",
				"audioLanguage",
			),
			Size: readString(item, "size", "fileSize"),
			Filename: readString(item, "filename", "fileName"),
			Type: readString(item, "type", "formatType"),
			Verified: false,
		}

		key := source.URL + "|" + source.Quality
		if seen[key] {
			continue
		}

		seen[key] = true
		sources = append(sources, source)
	}

	return sources, nil
}

// ParseMovieBoxSubtitles extracts subtitle metadata.
func ParseMovieBoxSubtitles(raw json.RawMessage) ([]MovieBoxSubtitle, error) {
	var root any

	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode subtitles: %w", err)
	}

	items := collectMaps(root)
	subtitles := make([]MovieBoxSubtitle, 0, len(items))
	seen := make(map[string]bool)

	for _, item := range items {
		url := readString(
			item,
			"url",
			"captionUrl",
			"subtitleUrl",
			"fileUrl",
		)

		if url == "" {
			continue
		}

		if seen[url] {
			continue
		}

		seen[url] = true

		subtitles = append(subtitles, MovieBoxSubtitle{
			Language: readString(
				item,
				"language",
				"lang",
				"languageName",
			),
			URL: url,
			Format: readString(
				item,
				"format",
				"fileType",
				"type",
			),
			Size: readString(item, "size", "fileSize"),
		})
	}

	return subtitles, nil
}

// findSubject recursively locates a subject object.
func findSubject(value any) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		if readString(v, "subjectId", "subject_id", "id") != "" &&
			readString(v, "title", "name", "subjectName") != "" {
			return v
		}

		for _, key := range []string{
			"data",
			"subject",
			"subjects",
			"result",
			"results",
			"items",
			"list",
		} {
			if child, ok := v[key]; ok {
				if found := findSubject(child); found != nil {
					return found
				}
			}
		}

		for _, child := range v {
			if found := findSubject(child); found != nil {
				return found
			}
		}

	case []any:
		for _, child := range v {
			if found := findSubject(child); found != nil {
				return found
			}
		}
	}

	return nil
}

// collectMaps recursively collects JSON objects from common response wrappers.
func collectMaps(value any) []map[string]any {
	result := make([]map[string]any, 0)

	var walk func(any)

	walk = func(current any) {
		switch v := current.(type) {
		case map[string]any:
			result = append(result, v)

			for _, child := range v {
				switch child.(type) {
				case map[string]any, []any:
					walk(child)
				}
			}

		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}

	walk(value)
	return result
}

// readString reads the first available string or numeric value.
func readString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := data[key]
		if !ok || value == nil {
			continue
		}

		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}

		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)

		case json.Number:
			return v.String()

		case int:
			return strconv.Itoa(v)
		}
	}

	return ""
}

// readInt reads the first available integer value.
func readInt(data map[string]any, keys ...string) int {
	for _, key := range keys {
		value, ok := data[key]
		if !ok || value == nil {
			continue
		}

		switch v := value.(type) {
		case int:
			return v

		case int64:
			return int(v)

		case float64:
			return int(v)

		case json.Number:
			n, err := v.Int64()
			if err == nil {
				return int(n)
			}

		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err == nil {
				return n
			}
		}
	}

	return 0
}
