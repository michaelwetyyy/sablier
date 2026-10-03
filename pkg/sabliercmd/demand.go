package sabliercmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sablierapp/sablier/pkg/demand"
	"github.com/spf13/cobra"
)

func newDemandCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "demand",
		Short: "Keep Sablier sessions alive while external work is pending",
		Long: `Demand bridges external work queues to Sablier sessions.
Each configured source is polled independently. While demand exists, the bridge
renews a Sablier poke session; when demand stops, the session expires normally
and Sablier applies its standard idle lifecycle to the target.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			demandConf, err := demand.LoadConfig(file)
			if err != nil {
				return err
			}
			logger := setupLogger(conf.Logging)
			manager, err := demand.NewManager(demandConf, logger)
			if err != nil {
				return fmt.Errorf("create demand manager: %w", err)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			var metricsServer *http.Server
			if listen := strings.TrimSpace(demandConf.Metrics.Listen); listen != "" {
				listener, err := net.Listen("tcp", listen)
				if err != nil {
					return fmt.Errorf("listen for demand metrics: %w", err)
				}
				metricsServer = &http.Server{
					Handler:           manager.MetricsHandler(),
					ReadHeaderTimeout: 5 * time.Second,
				}
				go func() {
					if serveErr := metricsServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
						logger.ErrorContext(ctx, "demand metrics server stopped", "error", serveErr)
					}
				}()
				defer func() {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					_ = metricsServer.Shutdown(shutdownCtx)
				}()
				logger.InfoContext(ctx, "demand metrics enabled", "listen", listener.Addr().String())
			}

			logger.InfoContext(ctx, "starting demand bridge", "sources", len(demandConf.Sources))
			return manager.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&file, "file", "/etc/sablier/demand.yaml", "Demand bridge configuration file")
	return cmd
}
