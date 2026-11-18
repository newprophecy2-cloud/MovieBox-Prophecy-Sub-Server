
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
	apiVersion  = "1.1.0"
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
	Upstream int    `json:"upstream_status,omitempty"`
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

	app.Use(recover.New())

	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,HEAD,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept,Authorization",
	}))

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

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":   "ok",
			"provider": apiProvider,
			"service":  "prophecy-moviebox",
		})
	})

	v1 := app.Group("/api/v1")
	anime := v1.Group("/anime")

	anime.Get("/search", api.search)
	anime.Get("/:id", api.details)
	anime.Get("/:id/episodes", api.episodes)
	anime.Get("/:id/sources", api.sources)
	anime.Get("/:id/subtitles", api.subtitles)

	app.Use(func(c *fiber.Ctx) error {
		return apiError(c, fiber.StatusNotFound, "Route not found")
	})

	log.Printf("MovieBox Prophecy server starting on port %s", port)

	if err := app.Listen(":" + port); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// SEARCH

func (a *API) search(c *fiber.Ctx) error {
	query := strings.TrimSpace(c.Query("q"))

	if query == "" {
		return apiError(c, 400, "Query parameter q is required")
	}

	if len(query) > 200 {
		return apiError(c, 400, "Search query is too long")
	}

	page, err := parseQueryInt(c, "page", 1)
	if err != nil || page < 1 || page > 10000 {
		return apiError(c, 400, "Invalid page")
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Search(ctx, query, page)
	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "search", result)
}

// DETAILS

func (a *API) details(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))

	if err := validateID(id); err != nil {
		return apiError(c, 400, err.Error())
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Details(ctx, id)
	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "details", result)
}

// EPISODES

func (a *API) episodes(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))

	if err := validateID(id); err != nil {
		return apiError(c, 400, err.Error())
	}

	season, err := parseQueryInt(c, "season", 1)
	if err != nil || season < 0 || season > 1000 {
		return apiError(c, 400, "Invalid season")
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Episodes(ctx, id, season)
	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "episodes", result)
}

// SOURCES

func (a *API) sources(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))

	if err := validateID(id); err != nil {
		return apiError(c, 400, err.Error())
	}

	season, err := parseQueryInt(c, "season", 1)
	if err != nil || season < 0 || season > 1000 {
		return apiError(c, 400, "Invalid season")
	}

	episode, err := parseQueryInt(c, "episode", 1)
	if err != nil || episode < 1 || episode > 100000 {
		return apiError(c, 400, "Invalid episode")
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Sources(ctx, id, season, episode)
	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "sources", result)
}

// SUBTITLES

func (a *API) subtitles(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))
	resourceID := strings.TrimSpace(c.Query("resource_id"))

	if err := validateID(id); err != nil {
		return apiError(c, 400, err.Error())
	}

	if err := validateID(resourceID); err != nil {
		return apiError(c, 400, "Valid resource_id is required")
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Subtitles(ctx, id, resourceID)
	if err != nil {
		return handleProviderError(c, err)
	}

	return success(c, "subtitles", result)
}

// REQUEST HELPERS

func (a *API) requestContext(c *fiber.Ctx) (
	context.Context,
	context.CancelFunc,
) {
	return context.WithTimeout(c.UserContext(), a.timeout)
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

// RESPONSE HELPERS

func success(c *fiber.Ctx, action string, data any) error {
	return c.JSON(APIResponse{
		Status:   "success",
		Provider: apiProvider,
		Action:   action,
		Data:     data,
	})
}

func apiError(c *fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(APIResponse{
		Status:   "error",
		Provider: apiProvider,
		Error:    message,
	})
}

func upstreamAPIError(
	c *fiber.Ctx,
	status int,
	message string,
) error {
	return c.Status(status).JSON(APIResponse{
		Status:   "error",
		Provider: apiProvider,
		Error:    message,
		Upstream: status,
	})
}

// PROVIDER ERROR HANDLING

func handleProviderError(c *fiber.Ctx, err error) error {
	if err == nil {
		return apiError(c, 500, "Unknown provider error")
	}

	if errors.Is(err, context.DeadlineExceeded) {
		log.Printf("MovieBox request timeout: %v", err)
		return apiError(c, 504, "MovieBox request timed out")
	}

	if errors.Is(err, context.Canceled) {
		return apiError(c, 408, "Request cancelled")
	}

	var upstreamErr *movieBoxHTTPError

	if errors.As(err, &upstreamErr) {
		status := upstreamErr.StatusCode
		message := strings.TrimSpace(upstreamErr.Message)

		log.Printf(
			"MovieBox upstream error: status=%d message=%s",
			status,
			message,
		)

		switch status {
		case 400:
			return upstreamAPIError(
				c, 502, "MovieBox rejected the request",
			)

		case 401:
			return upstreamAPIError(
				c, 502, "MovieBox authentication required",
			)

		case 403:
			if strings.Contains(
				strings.ToLower(message),
				"region",
			) {
				return upstreamAPIError(
					c,
					502,
					"MovieBox service is unavailable in the server's region",
				)
			}

			return upstreamAPIError(
				c, 502, "MovieBox denied the request",
			)

		case 404:
			return upstreamAPIError(
				c, 404, "MovieBox resource not found",
			)

		case 429:
			return upstreamAPIError(
				c, 503, "MovieBox upstream rate limit reached",
			)

		default:
			if status >= 500 {
				return upstreamAPIError(
					c, 502, "MovieBox upstream service unavailable",
				)
			}

			return upstreamAPIError(
				c, 502, "MovieBox upstream request failed",
			)
		}
	}

	log.Printf("MovieBox provider error: %v", err)

	return apiError(c, 502, "MovieBox provider request failed")
}

// Compile-time interface verification.

var _ MovieBoxService = (*MovieBoxClient)(nil)
