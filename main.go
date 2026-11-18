
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

const (
	ServiceName    = "MovieBox Prophecy Sub Server"
	ServiceVersion = "1.0.0"
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
}

func main() {
	moviebox := NewMovieBoxClient()

	api := &API{
		moviebox: moviebox,
	}

	app := fiber.New(fiber.Config{
		AppName:      ServiceName,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
		BodyLimit:    1 * 1024 * 1024,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError

			var fiberErr *fiber.Error
			if errors.As(err, &fiberErr) {
				code = fiberErr.Code
			}

			return c.Status(code).JSON(fiber.Map{
				"status":  "error",
				"message": http.StatusText(code),
			})
		},
	})

	app.Use(recover.New())
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,HEAD,OPTIONS",
		AllowHeaders: "Origin, Content-Type, Accept",
	}))

	app.Get("/", api.root)
	app.Get("/health", api.health)

	v1 := app.Group("/api/v1")

	anime := v1.Group("/anime")
	anime.Get("/search", api.search)
	anime.Get("/:id", api.details)
	anime.Get("/:id/episodes", api.episodes)
	anime.Get("/:id/sources", api.sources)
	anime.Get("/:id/subtitles", api.subtitles)

	port := strings.TrimSpace(getEnv("PORT", "10000"))

	log.Printf("%s starting on port %s", ServiceName, port)

	if err := app.Listen(":" + port); err != nil {
		log.Fatal(err)
	}
}

func (a *API) root(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"name":    ServiceName,
		"version": ServiceVersion,
		"status":  "online",
		"health":  "/health",
		"routes": fiber.Map{
			"search":    "/api/v1/anime/search?q=Naruto",
			"details":   "/api/v1/anime/{id}",
			"episodes":  "/api/v1/anime/{id}/episodes?season=1",
			"sources":   "/api/v1/anime/{id}/sources?season=1&episode=1",
			"subtitles": "/api/v1/anime/{id}/subtitles?resource_id={resource_id}",
		},
	})
}

func (a *API) health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status":   "ok",
		"provider": "moviebox",
		"service":  "prophecy-moviebox",
	})
}

func (a *API) search(c *fiber.Ctx) error {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"status":  "error",
			"message": "Query parameter q is required",
		})
	}

	page, err := queryInt(c, "page", 1)
	if err != nil || page < 1 {
		return badRequest(c, "page must be a positive integer")
	}

	ctx, cancel := requestContext(c)
	defer cancel()

	data, err := a.moviebox.Search(ctx, query, page)
	if err != nil {
		return providerError(c, err)
	}

	return c.JSON(fiber.Map{
		"status":   "success",
		"provider": "moviebox",
		"action":   "search",
		"data":     data,
	})
}

func (a *API) details(c *fiber.Ctx) error {
	subjectID := strings.TrimSpace(c.Params("id"))
	if subjectID == "" {
		return badRequest(c, "Anime ID is required")
	}

	ctx, cancel := requestContext(c)
	defer cancel()

	data, err := a.moviebox.Details(ctx, subjectID)
	if err != nil {
		return providerError(c, err)
	}

	return c.JSON(fiber.Map{
		"status":   "success",
		"provider": "moviebox",
		"action":   "details",
		"data":     data,
	})
}

func (a *API) episodes(c *fiber.Ctx) error {
	subjectID := strings.TrimSpace(c.Params("id"))
	if subjectID == "" {
		return badRequest(c, "Anime ID is required")
	}

	season, err := queryInt(c, "season", 1)
	if err != nil || season < 0 {
		return badRequest(c, "season must be zero or a positive integer")
	}

	ctx, cancel := requestContext(c)
	defer cancel()

	data, err := a.moviebox.Episodes(ctx, subjectID, season)
	if err != nil {
		return providerError(c, err)
	}

	return c.JSON(fiber.Map{
		"status":   "success",
		"provider": "moviebox",
		"action":   "episodes",
		"data":     data,
	})
}

func (a *API) sources(c *fiber.Ctx) error {
	subjectID := strings.TrimSpace(c.Params("id"))
	if subjectID == "" {
		return badRequest(c, "Anime ID is required")
	}

	season, err := queryInt(c, "season", 0)
	if err != nil || season < 0 {
		return badRequest(c, "Invalid season")
	}

	episode, err := queryInt(c, "episode", 0)
	if err != nil || episode < 0 {
		return badRequest(c, "Invalid episode")
	}

	if (season == 0) != (episode == 0) {
		return badRequest(c, "Season and episode must both be zero or both be positive")
	}

	ctx, cancel := requestContext(c)
	defer cancel()

	data, err := a.moviebox.Sources(ctx, subjectID, season, episode)
	if err != nil {
		return providerError(c, err)
	}

	return c.JSON(fiber.Map{
		"status":   "success",
		"provider": "moviebox",
		"action":   "sources",
		"data":     data,
	})
}

func (a *API) subtitles(c *fiber.Ctx) error {
	subjectID := strings.TrimSpace(c.Params("id"))
	resourceID := strings.TrimSpace(c.Query("resource_id"))

	if subjectID == "" || resourceID == "" {
		return badRequest(c, "Anime ID and resource_id are required")
	}

	ctx, cancel := requestContext(c)
	defer cancel()

	data, err := a.moviebox.Subtitles(ctx, subjectID, resourceID)
	if err != nil {
		return providerError(c, err)
	}

	return c.JSON(fiber.Map{
		"status":   "success",
		"provider": "moviebox",
		"action":   "subtitles",
		"data":     data,
	})
}

func queryInt(c *fiber.Ctx, key string, fallback int) (int, error) {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return fallback, nil
	}

	return strconv.Atoi(value)
}

func requestContext(c *fiber.Ctx) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 25*time.Second)
}

func badRequest(c *fiber.Ctx, message string) error {
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
		"status":  "error",
		"message": message,
	})
}

func providerError(c *fiber.Ctx, err error) error {
	log.Printf("MovieBox provider error: %v", err)

	return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
		"status":   "error",
		"provider": "moviebox",
		"message":  "MovieBox provider request failed",
	})
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(strings.TrimSpace(getenv(key)))
	if value == "" {
		return fallback
	}
	return value
}
