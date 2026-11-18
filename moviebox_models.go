
package main

import "encoding/json"

// MovieBoxSearchResponse represents normalized search results.
type MovieBoxSearchResponse struct {
	Subjects []MovieBoxSubject `json:"subjects"`
	Total    int                `json:"total,omitempty"`
	Page     int                `json:"page,omitempty"`
}

// MovieBoxSubject represents a MovieBox catalog item.
type MovieBoxSubject struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Type        int      `json:"subject_type,omitempty"`
	Year        string   `json:"year,omitempty"`
	Poster      string   `json:"poster,omitempty"`
	Description string   `json:"description,omitempty"`
	Rating      string   `json:"rating,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	SeasonCount int      `json:"season_count,omitempty"`
}

// MovieBoxDetails represents subject details.
type MovieBoxDetails struct {
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Type        int                 `json:"subject_type,omitempty"`
	Year        string              `json:"year,omitempty"`
	Poster      string              `json:"poster,omitempty"`
	Description string              `json:"description,omitempty"`
	Rating      string              `json:"rating,omitempty"`
	Director    string              `json:"director,omitempty"`
	Cast        []string            `json:"cast,omitempty"`
	Genres      []string            `json:"genres,omitempty"`
	Seasons     []MovieBoxSeason    `json:"seasons,omitempty"`
}

// MovieBoxSeason represents one season.
type MovieBoxSeason struct {
	Number        int    `json:"season"`
	EpisodeCount  int    `json:"episode_count,omitempty"`
	Language      string `json:"language,omitempty"`
	SubjectID     string `json:"subject_id,omitempty"`
}

// MovieBoxEpisode represents episode metadata.
type MovieBoxEpisode struct {
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Title   string `json:"title,omitempty"`
	Poster  string `json:"poster,omitempty"`
}

// MovieBoxSource represents a candidate playback resource.
// Verified must remain false until actual playback is tested.
type MovieBoxSource struct {
	ID       string            `json:"id,omitempty"`
	URL      string            `json:"url"`
	Quality  string            `json:"quality,omitempty"`
	Codec    string            `json:"codec,omitempty"`
	Language string            `json:"language,omitempty"`
	Size     string            `json:"size,omitempty"`
	Filename string            `json:"filename,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Type     string            `json:"type"`
	Verified bool              `json:"verified"`
}

// MovieBoxSubtitle represents an external subtitle.
type MovieBoxSubtitle struct {
	Language string `json:"language,omitempty"`
	URL      string `json:"url"`
	Format   string `json:"format,omitempty"`
	Size     string `json:"size,omitempty"`
}

// MovieBoxEnvelope is a generic API envelope.
type MovieBoxEnvelope struct {
	Status   string          `json:"status,omitempty"`
	Message  string          `json:"message,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Subjects []MovieBoxSubject `json:"subjects,omitempty"`
}
