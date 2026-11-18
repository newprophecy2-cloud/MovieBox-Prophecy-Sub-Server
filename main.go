
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
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
		port = "10000"
	}

	timeout := 25 * time.Second

	if value := os.Getenv("REQUEST_TIMEOUT"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil &&
			seconds >= 1 && seconds <= 120 {
			timeout = time.Duration(seconds) * time.Second
		}
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

	// Root.
	app.Get("/", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"name":    "MovieBox Prophecy Sub Server",
			"version": "1.0.0",
			"status":  "online",
			"health":  "/health",
			"provider": "moviebox",
			"routes": fiber.Map{
				"search":    "/api/v1/anime/search?q=Naruto",
				"details":   "/api/v1/anime/:id",
				"episodes":  "/api/v1/anime/:id/episodes?season=1",
				"sources":   "/api/v1/anime/:id/sources?season=1&episode=1",
				"subtitles": "/api/v1/anime/:id/subtitles?resource_id=RESOURCE_ID",
			},
		})
	})

	// Health.
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":   "ok",
			"provider": "moviebox",
			"service":  "prophecy-moviebox",
		})
	})

	v1 := app.Group("/api/v1")
	anime := v1.Group("/anime")

	// Search.
	anime.Get("/search", api.search)

	// Details.
	anime.Get("/:id", api.details)

	// Episodes.
	anime.Get("/:id/episodes", api.episodes)

	// Playback metadata and resources.
	anime.Get("/:id/sources", api.sources)

	// Subtitles.
	anime.Get("/:id/subtitles", api.subtitles)

	// Unknown route.
	app.Use(func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(APIResponse{
			Status:   "error",
			Provider: "moviebox",
			Error:    "Route not found",
		})
	})

	log.Printf("MovieBox Prophecy server listening on :%s", port)

	if err := app.Listen(":" + port); err != nil {
		log.Fatal(err)
	}
}

func (a *API) search(c *fiber.Ctx) error {
	query := strings.TrimSpace(c.Query("q"))

	if query == "" {
		return apiError(c, fiber.StatusBadRequest, "Query parameter q is required")
	}

	page := c.QueryInt("page", 1)

	if page < 1 || page > 10000 {
		return apiError(c, fiber.StatusBadRequest, "Invalid page")
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Search(ctx, query, page)
	if err != nil {
		return handleProviderError(c, err)
	}

	return c.JSON(APIResponse{
		Status:   "success",
		Provider: "moviebox",
		Action:   "search",
		Data:     result,
	})
}

func (a *API) details(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))

	if id == "" {
		return apiError(c, fiber.StatusBadRequest, "Anime ID is required")
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Details(ctx, id)
	if err != nil {
		return handleProviderError(c, err)
	}

	return c.JSON(APIResponse{
		Status:   "success",
		Provider: "moviebox",
		Action:   "details",
		Data:     result,
	})
}

func (a *API) episodes(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))
	season := c.QueryInt("season", 1)

	if id == "" {
		return apiError(c, fiber.StatusBadRequest, "Anime ID is required")
	}

	if season < 0 || season > 1000 {
		return apiError(c, fiber.StatusBadRequest, "Invalid season")
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Episodes(ctx, id, season)
	if err != nil {
		return handleProviderError(c, err)
	}

	return c.JSON(APIResponse{
		Status:   "success",
		Provider: "moviebox",
		Action:   "episodes",
		Data:     result,
	})
}

func (a *API) sources(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))
	season := c.QueryInt("season", 1)
	episode := c.QueryInt("episode", 1)

	if id == "" {
		return apiError(c, fiber.StatusBadRequest, "Anime ID is required")
	}

	if season < 0 || season > 1000 {
		return apiError(c, fiber.StatusBadRequest, "Invalid season")
	}

	if episode < 1 || episode > 100000 {
		return apiError(c, fiber.StatusBadRequest, "Invalid episode")
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Sources(ctx, id, season, episode)
	if err != nil {
		return handleProviderError(c, err)
	}

	return c.JSON(APIResponse{
		Status:   "success",
		Provider: "moviebox",
		Action:   "sources",
		Data:     result,
	})
}

func (a *API) subtitles(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))
	resourceID := strings.TrimSpace(c.Query("resource_id"))

	if id == "" || resourceID == "" {
		return apiError(
			c,
			fiber.StatusBadRequest,
			"Anime ID and resource_id are required",
		)
	}

	ctx, cancel := a.requestContext(c)
	defer cancel()

	result, err := a.moviebox.Subtitles(ctx, id, resourceID)
	if err != nil {
		return handleProviderError(c, err)
	}

	return c.JSON(APIResponse{
		Status:   "success",
		Provider: "moviebox",
		Action:   "subtitles",
		Data:     result,
	})
}

func (a *API) requestContext(c *fiber.Ctx) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), a.timeout)
}

func apiError(c *fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(APIResponse{
		Status:   "error",
		Provider: "moviebox",
		Error:    message,
	})
}

func handleProviderError(c *fiber.Ctx, err error) error {
	if err == nil {
		return apiError(c, fiber.StatusInternalServerError, "Unknown provider error")
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return apiError(c, fiber.StatusGatewayTimeout, "MovieBox request timed out")
	}

	if errors.Is(err, context.Canceled) {
		return apiError(c, fiber.StatusRequestTimeout, "Request cancelled")
	}

	message := err.Error()

	status := fiber.StatusBadGateway

	if strings.Contains(message, "required") ||
		strings.Contains(message, "invalid") {
		status = fiber.StatusBadRequest
	}

	log.Printf("MovieBox provider error: %v", err)

	return c.Status(status).JSON(APIResponse{
		Status:   "error",
		Provider: "moviebox",
		Error:    message,
	})
}

var _ = fmt.Sprintf
