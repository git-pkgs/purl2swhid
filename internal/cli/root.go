package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/git-pkgs/purl2swhid"
	"github.com/spf13/cobra"
)

var version = "dev"

const defaultTimeout = 5 * time.Minute

func Root() *cobra.Command {
	return newRoot(nil)
}

func newRoot(client *http.Client) *cobra.Command {
	opts := purl2swhid.Options{HTTPClient: client}
	var jsonOutput bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:           "purl2swhid <package-url>",
		Short:         "Compute a Software Heritage identifier for a published package",
		Long:          "Download a package URL and hash its unpacked contents.\nAn omitted version resolves to the registry's latest release.\nThe default output is one SWHID. Archive lookups and save requests are optional.",
		Version:       buildVersion(),
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				return errors.New("timeout must be positive")
			}
			opts.Token = os.Getenv("SWH_TOKEN")
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			result, err := purl2swhid.Resolve(ctx, args[0], opts)
			if result == nil {
				return err
			}
			return errors.Join(err, writeResult(cmd, result, jsonOutput))
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output JSON with package, archive and preservation details")
	cmd.Flags().BoolVar(&opts.Check, "check", false, "check whether the SWHID is in Software Heritage")
	cmd.Flags().BoolVar(&opts.Save, "save", false, "check and request archival of the download URL if the SWHID is missing")
	cmd.Flags().BoolVar(&opts.KeepRoot, "keep-root", false, "keep the archive wrapper instead of selecting the package root")
	cmd.Flags().StringVar(&opts.Filename, "filename", "", "select an exact artifact filename when a version has several")
	cmd.Flags().StringVar(&opts.SWHURL, "swh-url", "", "Software Heritage API base URL")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultTimeout, "timeout for the entire operation")
	return cmd
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func writeResult(cmd *cobra.Command, result *purl2swhid.Result, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), result.SWHID); err != nil {
		return err
	}
	if result.Known != nil {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Known to Software Heritage: %t\n", *result.Known); err != nil {
			return err
		}
	}
	if result.Save != nil {
		status := result.Save.Status
		if result.Save.Task != "" {
			status += " (" + result.Save.Task + ")"
		}
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Save request %d: %s\n", result.Save.ID, status)
		return err
	}
	return nil
}
