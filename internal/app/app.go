package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/capcom6/swarm-gateway-tutorial/internal/discovery"
	"github.com/capcom6/swarm-gateway-tutorial/internal/repository"
	"github.com/docker/docker/client"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/proxy"
	"github.com/valyala/fasthttp"
)

func Run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	var wg sync.WaitGroup

	servicesRepo := repository.NewServicesRepository()
	if err := startDiscovery(ctx, &wg, servicesRepo); err != nil {
		return err
	}
	if err := startProxy(ctx, &wg, servicesRepo); err != nil {
		return err
	}

	wg.Wait()

	log.Println("Done")

	return nil
}

func startDiscovery(ctx context.Context, wg *sync.WaitGroup, servicesRepo *repository.ServicesRepository) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}

	wg.Add(1)
	go func() {
		discoverySvc := discovery.NewSwarmDiscovery(cli)
		timer := time.NewTicker(5 * time.Second)
		defer func() {
			timer.Stop()
			cli.Close()
			wg.Done()
		}()

		for {
			select {
			case <-ctx.Done():
				log.Println("Discovery Done")
				return
			case <-timer.C:
				timeoutCtx, cancel := context.WithTimeout(ctx, time.Second)
				services, err := discoverySvc.ListServices(timeoutCtx)
				if err != nil {
					log.Println(err)
				}
				cancel()

				servicesRepo.ReplaceServices(services)
			}
		}
	}()

	return nil
}

func startProxy(ctx context.Context, wg *sync.WaitGroup, servicesRepo *repository.ServicesRepository) error {
	app := fiber.New()

	// app.Get("/", func(c *fiber.Ctx) error {
	// 	return c.SendString("Hello, World!")
	// })

	app.Get("/*", func(c *fiber.Ctx) error {
		host := c.Get("Host")
		if host == "" {
			return fiber.ErrBadRequest
		}

		service, err := servicesRepo.GetServiceByHost(host)
		if errors.Is(err, repository.ErrSeviceNotFound) {
			return fiber.ErrBadGateway
		}

		url := fmt.Sprintf("http://%s:%d/%s", service.Name, service.Port, c.Params("*"))

		if err := proxy.DoTimeout(c, url, 5*time.Second); err != nil {
			log.Printf("proxy error: %s", err)
			if errors.Is(err, fasthttp.ErrTimeout) {
				return fiber.ErrGatewayTimeout
			}
			return err
		}
		// Remove Server header from response
		c.Response().Header.Del(fiber.HeaderServer)
		return nil
	})

	wg.Add(1)
	go func() {
		if err := app.Listen(":3000"); err != nil {
			log.Printf("can't listen: %s", err)
		}

		wg.Done()
	}()

	wg.Add(1)
	go func() {
		<-ctx.Done()

		app.Shutdown()

		wg.Done()
	}()

	return nil
}
