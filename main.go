
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

const (
	apiProvider = "moviebox"
	apiVersion  = "1.0.0"
	defaultPort = "10000"
)

type MovieBoxService interface {
	Search(ctx context.Context, query string, page int) (any, error)
	Details(ctx context.Context, subjectID string) (any, error)
	Episodes(ctx context.Context, subjectID string, season int) (any, error)
	Sources(ctx context.Context, subjectID string, season, episode int) (any, error)
	Subtitles(ctx context.Context, subjectID, resourceID string) (any, error)
}

type API struct {
	moviebox MovieBoxService
	timeout  time.Duration
}

type APIResponse struct {
	Status   string `json:"status"`
	Provider string `json:"provider"`
	Action   string `json:"action,omitempty"`
	Data     any    `json:"data,omitempty"`
	Error    string `json:"error,omitempty"`
}

func main() {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = defaultPort
	}

	timeout := 25 * time.Second

	if value := strings.TrimSpace(os.Getenv("REQUEST_TIMEOUT")); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 1 || seconds > 120 {
			log.Fatal("REQUEST_TIMEOUT must be between 1 and 120 seconds")
		}
		timeout = time.Duration(seconds) * time.Second
	}

	client := NewMovieBoxClient()

	api := &API{
		moviebox: client,
		timeout:  timeout,
	}

	app := fiber.New(fiber.Config{
		AppName:               "MovieBox Prophecy Sub Server",
		ReadTimeout:           30 * time.Second,
		WriteTimeout:          30 * time.Second,
		IdleTimeout:           60 * time.Second,
		BodyLimit:             1 * 1024 * 1024,
		DisableStartupMessage: true,
	})

	// Panic recovery.
	app.Use(recover.New())

	// CORS.
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,HEAD,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept,Authorization",
	}))

	// Request logging.
	app.Use(func(c *fiber.Ctx) error {
		start := time.Now()

		err := c.Next()

		log.Printf(
			"method=%s path=%s status=%d duration=%s",
			c.Method(),
			c.Path(),
			c.Response().StatusCode(),
			time.Since(start),
		)

		return err
	})

	// Root.
	app.Get("/", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"name":     "MovieBox Prophecy Sub Server",
			"version":  apiVersion,
			"provider": apiProvider,
			"status":   "online",
			"health":   "/health",
			"routes": fiber.Map{
				"search":    "/api/v1/anime/search?q=Naruto&page=1",
				"details":   "/api/v1/anime/:id",
				"episodes":  "/api/v1/anime/:id/episodes?season=1",
				"sources":   "/api/v1/anime/:id/sources?season=1&episode=1",
				"subtitles": "/api/v1/anime/:id/subtitles?resource_id=RESOURCE_ID",
			},
		})
	})

	// Local health check.
	// This does not verify upstream MovieBox availability.
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":   "ok",
			"provider": apiProvider,
			"service":  "prophecy-moviebox",
		})
	})

	// API routes.
	v1 := app.Group("/api/v1")
	anime := v1.Group("/anime")

	anime.Get("/search", api.search)
	anime.Get("/:id", api.details)
	anime.Get("/:id/episodes", api.episodes)
	anime.Get("/:id/sources", api.sources)
	anime.Get("/:id/subtitles", api.subtitles)

	// Unknown routes.
	app.Use(func(c *fiber.Ctx) error {
		return apiError(
			c,
			fiber.StatusNotFound,
			"Route not found",
		)
	})

	log.Printf("MovieBox Prophecy server starting on port %s", port)

	if err := app.Listen(":" + port); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// --------------------------------------------------
// SEARCH
// GET /api/v1/anime/search?q=Naruto&page=1
// --------------------------------------------------

func (a *API) search(c *fiber.Ctx) error {
	query := strings.TrimSpace(c.Query("q"))

	if query == "" {
		return apiError(
			c,
			fiber.StatusBadRequest,
			"Query parameter q is required",
		)
	}

	if len(query) > 200 {
		return apiError(
			c,
			fiber.StatusBadRequest,
			"Search query is too long",
		)
	}

	page, err := parseQueryInt(c, "page", 1)
	if err != nil || page < 1 || page > 10000 {
		return apiError(
			c,
			fiber.StatusBadRequest,
			"Invalid page",
		)
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Search(ctx, query, page)
	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "search", result)
}

// --------------------------------------------------
// DETAILS
// GET /api/v1/anime/:id
// --------------------------------------------------

func (a *API) details(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))

	if err := validateID(id); err != nil {
		return apiError(
			c,
			fiber.StatusBadRequest,
			err.Error(),
		)
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Details(ctx, id)
	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "details", result)
}

// --------------------------------------------------
// EPISODES
// GET /api/v1/anime/:id/episodes?season=1
// --------------------------------------------------

func (a *API) episodes(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))

	if err := validateID(id); err != nil {
		return apiError(
			c,
			fiber.StatusBadRequest,
			err.Error(),
		)
	}

	season, err := parseQueryInt(c, "season", 1)
	if err != nil || season < 0 || season > 1000 {
		return apiError(
			c,
			fiber.StatusBadRequest,
			"Invalid season",
		)
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Episodes(ctx, id, season)
	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "episodes", result)
}

// --------------------------------------------------
// SOURCES
// GET /api/v1/anime/:id/sources?season=1&episode=1
// --------------------------------------------------

func (a *API) sources(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))

	if err := validateID(id); err != nil {
		return apiError(
			c,
			fiber.StatusBadRequest,
			err.Error(),
		)
	}

	season, err := parseQueryInt(c, "season", 1)
	if err != nil || season < 0 || season > 1000 {
		return apiError(
			c,
			fiber.StatusBadRequest,
			"Invalid season",
		)
	}

	episode, err := parseQueryInt(c, "episode", 1)
	if err != nil || episode < 1 || episode > 100000 {
		return apiError(
			c,
			fiber.StatusBadRequest,
			"Invalid episode",
		)
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Sources(
		ctx,
		id,
		season,
		episode,
	)

	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "sources", result)
}

// --------------------------------------------------
// SUBTITLES
// GET /api/v1/anime/:id/subtitles?resource_id=...
// --------------------------------------------------

func (a *API) subtitles(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))
	resourceID := strings.TrimSpace(c.Query("resource_id"))

	if err := validateID(id); err != nil {
		return apiError(
			c,
			fiber.StatusBadRequest,
			err.Error(),
		)
	}

	if err := validateID(resourceID); err != nil {
		return apiError(
			c,
			fiber.StatusBadRequest,
			"Valid resource_id is required",
		)
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Subtitles(
		ctx,
		id,
		resourceID,
	)

	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "subtitles", result)
}

// --------------------------------------------------
// REQUEST HELPERS
// --------------------------------------------------

func (a *API) requestContext(c *fiber.Ctx) (
	context.Context,
	context.CancelFunc,
) {
	return context.WithTimeout(
		c.UserContext(),
		a.timeout,
	)
}

func parseQueryInt(
	c *fiber.Ctx,
	key string,
	defaultValue int,
) (int, error) {
	raw := strings.TrimSpace(c.Query(key))

	if raw == "" {
		return defaultValue, nil
	}

	return strconv.Atoi(raw)
}

// --------------------------------------------------
// RESPONSE HELPERS
// --------------------------------------------------

func success(
	c *fiber.Ctx,
	action string,
	data any,
) error {
	return c.JSON(APIResponse{
		Status:   "success",
		Provider: apiProvider,
		Action:   action,
		Data:     data,
	})
}

func apiError(
	c *fiber.Ctx,
	status int,
	message string,
) error {
	return c.Status(status).JSON(APIResponse{
		Status:   "error",
		Provider: apiProvider,
		Error:    message,
	})
}

// --------------------------------------------------
// PROVIDER ERROR HANDLING
// --------------------------------------------------

func handleProviderError(
	c *fiber.Ctx,
	err error,
) error {
	if err == nil {
		return apiError(
			c,
			fiber.StatusInternalServerError,
			"Unknown provider error",
		)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		log.Printf("MovieBox request timeout: %v", err)

		return apiError(
			c,
			fiber.StatusGatewayTimeout,
			"MovieBox request timed out",
		)
	}

	if errors.Is(err, context.Canceled) {
		return apiError(
			c,
			fiber.StatusRequestTimeout,
			"Request cancelled",
		)
	}

	var upstreamErr *movieBoxHTTPError

	if errors.As(err, &upstreamErr) {
		log.Printf(
			"MovieBox upstream error: status=%d message=%s",
			upstreamErr.StatusCode,
			upstreamErr.Message,
		)

		switch upstreamErr.StatusCode {
		case 400:
			return apiError(
				c,
				fiber.StatusBadGateway,
				"MovieBox rejected the request",
			)

		case 401, 403:
			return apiError(
				c,
				fiber.StatusBadGateway,
				"MovieBox upstream authorization failed",
			)

		case 404:
			return apiError(
				c,
				fiber.StatusNotFound,
				"MovieBox resource not found",
			)

		case 429:
			return apiError(
				c,
				fiber.StatusServiceUnavailable,
				"MovieBox upstream rate limit reached",
			)

		default:
			if upstreamErr.StatusCode >= 500 {
				return apiError(
					c,
					fiber.StatusBadGateway,
					"MovieBox upstream service unavailable",
				)
			}
		}
	}

	log.Printf("MovieBox provider error: %v", err)

	return apiError(
		c,
		fiber.StatusBadGateway,
		"MovieBox provider request failed",
	)
}

// Compile-time interface verification.
var _ MovieBoxService = (*MovieBoxClient)(nil)
