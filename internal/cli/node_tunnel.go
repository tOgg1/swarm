package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/ssh"
	"github.com/opencode-ai/swarm/internal/swarmd"
	"github.com/spf13/cobra"
)

var (
	nodeTunnelLocalHost string
	nodeTunnelLocalPort int
	nodeTunnelRemote    string
)

func init() {
	nodeCmd.AddCommand(nodeTunnelCmd)

	defaultRemote := fmt.Sprintf("%s:%d", swarmd.DefaultHost, swarmd.DefaultPort)
	nodeTunnelCmd.Flags().StringVar(&nodeTunnelRemote, "remote", defaultRemote, "remote swarmd host:port")
	nodeTunnelCmd.Flags().IntVar(&nodeTunnelLocalPort, "local-port", 0, "local port to bind (defaults to remote port)")
	nodeTunnelCmd.Flags().StringVar(&nodeTunnelLocalHost, "local-host", "127.0.0.1", "local host to bind")
}

var nodeTunnelCmd = &cobra.Command{
	Use:   "tunnel <name-or-id>",
	Short: "Create an SSH tunnel to swarmd",
	Long: `Create an SSH tunnel to a node's swarmd service.

This is a convenience wrapper around port forwarding using the swarmd defaults.`,
	Example: `  # Forward local 50051 to swarmd on a remote node
  swarm node tunnel prod-server

  # Bind a custom local port
  swarm node tunnel prod-server --local-port 55051

  # Override the remote swarmd bind address
  swarm node tunnel prod-server --remote 127.0.0.1:60000`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		nameOrID := args[0]
		remoteHost, remotePort, err := parseForwardTarget(nodeTunnelRemote)
		if err != nil {
			return err
		}

		localPort := nodeTunnelLocalPort
		if !cmd.Flags().Changed("local-port") {
			localPort = remotePort
		}
		if localPort < 1 || localPort > 65535 {
			return fmt.Errorf("local port must be between 1 and 65535")
		}

		database, err := openDatabase()
		if err != nil {
			return err
		}
		defer database.Close()

		repo := db.NewNodeRepository(database)
		service := node.NewService(repo)

		n, err := findNode(ctx, service, nameOrID)
		if err != nil {
			return err
		}

		spec := ssh.PortForwardSpec{
			LocalHost:  nodeTunnelLocalHost,
			LocalPort:  localPort,
			RemoteHost: remoteHost,
			RemotePort: remotePort,
		}

		forward, err := service.StartPortForward(ctx, n, spec)
		if err != nil {
			return err
		}
		defer forward.Close()

		if IsJSONOutput() || IsJSONLOutput() {
			if err := WriteOutput(os.Stdout, map[string]any{
				"node_id":     n.ID,
				"node_name":   n.Name,
				"local_addr":  forward.LocalAddr(),
				"remote_addr": forward.RemoteAddr(),
			}); err != nil {
				return err
			}
		} else {
			fmt.Printf("Swarmd tunnel %s -> %s via %s. Press Ctrl+C to stop.\n", forward.LocalAddr(), forward.RemoteAddr(), n.Name)
		}

		err = forward.Wait()
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("tunnel stopped: %w", err)
		}
		return nil
	},
}
