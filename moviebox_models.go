
package main

// --------------------------------------------------
// API ENVELOPE
// --------------------------------------------------

type MovieBoxEnvelope struct {
	Status   string `json:"status,omitempty"`
	Code     int    `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	Data     any    `json:"data,omitempty"`
	Subjects []MovieBoxSubject `json:"subjects,omitempty"`
}

// --------------------------------------------------
// SEARCH
// --------------------------------------------------

type MovieBoxSearchResponse struct {
	Subjects []MovieBoxSubject `json:"subjects"`
	Total    int               `json:"total,omitempty"`
	Page     int               `json:"page,omitempty"`
	HasMore  bool              `json:"has_more,omitempty"`
}

type MovieBoxSubject struct {
	ID          string   `json:"id"`
	SubjectID   string   `json:"subject_id,omitempty"`
	Title       string   `json:"title"`
	OriginalTitle string `json:"original_title,omitempty"`
	Type        int      `json:"subject_type,omitempty"`
	MediaType   string   `json:"media_type,omitempty"`
	Year        string   `json:"year,omitempty"`
	Poster      string   `json:"poster,omitempty"`
	Backdrop    string   `json:"backdrop,omitempty"`
	Description string   `json:"description,omitempty"`
	Rating      string   `json:"rating,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	SeasonCount int      `json:"season_count,omitempty"`
	EpisodeCount int     `json:"episode_count,omitempty"`
	Duration    string   `json:"duration,omitempty"`
	Language    string   `json:"language,omitempty"`
}

// --------------------------------------------------
// DETAILS
// --------------------------------------------------

type MovieBoxDetails struct {
	ID          string           `json:"id"`
	SubjectID   string           `json:"subject_id,omitempty"`
	Title       string           `json:"title"`
	OriginalTitle string         `json:"original_title,omitempty"`
	Type        int              `json:"subject_type,omitempty"`
	MediaType   string           `json:"media_type,omitempty"`
	Year        string           `json:"year,omitempty"`
	Poster      string           `json:"poster,omitempty"`
	Backdrop    string           `json:"backdrop,omitempty"`
	Description string           `json:"description,omitempty"`
	Rating      string           `json:"rating,omitempty"`
	Director    string           `json:"director,omitempty"`
	Cast        []string         `json:"cast,omitempty"`
	Genres      []string         `json:"genres,omitempty"`
	Duration    string           `json:"duration,omitempty"`
	Language    string           `json:"language,omitempty"`
	Seasons     []MovieBoxSeason `json:"seasons,omitempty"`
}

// --------------------------------------------------
// SEASONS
// --------------------------------------------------

type MovieBoxSeason struct {
	Number       int    `json:"season"`
	SeasonNumber int    `json:"season_number,omitempty"`
	SeasonID     string `json:"season_id,omitempty"`
	EpisodeCount int    `json:"episode_count,omitempty"`
	Language     string `json:"language,omitempty"`
	SubjectID    string `json:"subject_id,omitempty"`
	Title        string `json:"title,omitempty"`
	Poster       string `json:"poster,omitempty"`
}

// --------------------------------------------------
// EPISODES
// --------------------------------------------------

type MovieBoxEpisodeResponse struct {
	Provider     string           `json:"provider"`
	SubjectID    string           `json:"subject_id"`
	Season       int              `json:"season"`
	EpisodeCount int              `json:"episode_count"`
	Episodes     []MovieBoxEpisode `json:"episodes"`
}

type MovieBoxEpisode struct {
	ID          string `json:"id,omitempty"`
	EpisodeID   string `json:"episode_id,omitempty"`
	Season      int    `json:"season"`
	Episode     int    `json:"episode"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Poster      string `json:"poster,omitempty"`
	Duration    string `json:"duration,omitempty"`
	AirDate     string `json:"air_date,omitempty"`
}

// --------------------------------------------------
// SOURCES
// --------------------------------------------------

type MovieBoxSourcesResponse struct {
	Provider  string           `json:"provider"`
	SubjectID string           `json:"subject_id"`
	Season    int              `json:"season"`
	Episode   int              `json:"episode"`
	Status    string           `json:"status"`
	Available bool             `json:"available"`
	Verified  bool             `json:"verified"`
	Sources   []MovieBoxSource `json:"sources"`
}

type MovieBoxSource struct {
	ID       string            `json:"id,omitempty"`
	ResourceID string          `json:"resource_id,omitempty"`
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

// --------------------------------------------------
// SUBTITLES
// --------------------------------------------------

type MovieBoxSubtitlesResponse struct {
	Provider   string             `json:"provider"`
	SubjectID  string             `json:"subject_id"`
	ResourceID string             `json:"resource_id"`
	Subtitles  []MovieBoxSubtitle `json:"subtitles"`
}

type MovieBoxSubtitle struct {
	ID       string `json:"id,omitempty"`
	Language string `json:"language,omitempty"`
	URL      string `json:"url"`
	Format   string `json:"format,omitempty"`
	Size     string `json:"size,omitempty"`
}
