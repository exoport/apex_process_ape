package apecmd

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/spf13/cobra"
)

func newTranscriptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transcript",
		Short: "Work with Claude session transcripts",
		Long:  "Transcript utilities. The upload subcommand blob-uploads a session's transcript set over NATS.",
	}
	cmd.AddCommand(newTranscriptUploadCmd())
	return cmd
}

func newTranscriptUploadCmd() *cobra.Command {
	var (
		f     reportFlags
		store string
	)
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload this session's transcript set as content-addressed blobs",
		Long: `Upload the resolved Claude session set (main + sub-agents) as
deduplicated, content-addressed, zstd-compressed blobs, then publish a
companion ape.evt.<user>.<project>.session.<session-id>.transcript-uploaded
event carrying the digest map.

Uploading is idempotent: a blob already present is a cheap no-op (its result
entry is marked existed=true with the same digest), so re-running is safe.

--store selects the backend: nats-object (a NATS JetStream Object Store,
default) or uri-offload (a NATS request returns a signed upload URI; ape
does the HTTPS PUT).

Exit codes: 0 uploaded · 1 upload/publish failed (connected) · 2 usage
error, no NATS configured, or the session was unresolvable.`,
		Example: `  ape transcript upload
  ape transcript upload --store uri-offload --output-format json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReport(func() error {
				return runTranscriptUpload(cmd.Context(), cmd.OutOrStdout(), &f, store)
			})
		},
	}
	cmd.Flags().StringVar(&store, "store", "nats-object", "Blob backend: nats-object|uri-offload (env APE_TRANSCRIPT_STORE).")
	addReportFlags(cmd, &f, true)
	return cmd
}

func runTranscriptUpload(ctx context.Context, out io.Writer, f *reportFlags, store string) error {
	r, ref, err := setupReporter(ctx, f)
	if err != nil {
		return err
	}
	defer r.Close()

	if ref.Transcript == "" {
		return usageErr(fmt.Errorf("session %s has no transcript on disk; pass --transcript", ref.SessionID))
	}
	project, _ := f.projectRoot()
	blobStore, err := newTranscriptStore(ctx, r.Conn(), project, ref.SessionID, store)
	if err != nil {
		return usageErr(err)
	}

	files := cost.SessionFiles(ref.Transcript, time.Time{})
	result, err := r.UploadTranscripts(ctx, blobStore, ref.SessionID, files)
	if err != nil {
		return failErr(err)
	}
	if err := r.PublishTranscriptUploaded(ref.SessionID, result); err != nil {
		return failErr(err)
	}

	if f.jsonMode() {
		return emitJSON(out, result)
	}
	if !f.quiet {
		existed := 0
		for _, fl := range result.Files {
			if fl.Existed {
				existed++
			}
		}
		fmt.Fprintf(out, "✅ uploaded %d transcript(s) for session %s (%d already present)\n",
			len(result.Files), ref.SessionID, existed)
	}
	return nil
}
