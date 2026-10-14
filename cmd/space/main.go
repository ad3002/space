// Command space is a disk-usage viewer for large (genomic) storage built on
// ncdu exports. See README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"space/internal/server"
	"space/internal/store"
	"space/web"
)

var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `space %s — disk usage viewer for ncdu snapshots

Usage:
  space serve [-dir ./snapshots] [-listen 127.0.0.1:8377] [-keep 2]
  space index [-dir ./snapshots]     index all snapshots now and report
  space version

Take snapshots with scripts/collect.sh (needs only sudo ncdu).
Open the UI from your laptop through an SSH tunnel:
  ssh -N -L 8377:127.0.0.1:8377 <server>   then open http://localhost:8377
`, version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	switch os.Args[1] {
	case "serve":
		os.Exit(serve(os.Args[2:], log))
	case "index":
		os.Exit(indexCmd(os.Args[2:], log))
	case "version", "-v", "--version":
		fmt.Println(version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func serve(args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := fs.String("dir", "./snapshots", "directory with ncdu snapshots")
	listen := fs.String("listen", "127.0.0.1:8377", "listen address (keep it on localhost and use an SSH tunnel)")
	keep := fs.Int("keep", 2, "how many full indexes to keep in memory")
	fs.Parse(args)

	st, err := store.Open(*dir, *keep, log)
	if err != nil {
		log.Error("cannot open snapshot directory", "err", err)
		return 1
	}
	if err := st.Refresh(); err != nil {
		log.Error("cannot scan snapshot directory", "err", err)
		return 1
	}
	stop := make(chan struct{})
	go st.Run(stop)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("cannot listen", "addr", *listen, "err", err)
		return 1
	}
	srv := &http.Server{Handler: server.New(st, web.Static(), log), ReadHeaderTimeout: 10 * time.Second}
	host, _ := os.Hostname()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	log.Info("serving", "addr", ln.Addr().String(), "snapshots", *dir)
	fmt.Fprintf(os.Stderr, "\nFrom your Mac:\n  ssh -N -L %s:127.0.0.1:%s %s\n  open http://localhost:%s\n\n", port, port, host, port)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			return 1
		}
	}
	close(stop)
	shut, c2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer c2()
	if err := srv.Shutdown(shut); err != nil {
		log.Error("shutdown", "err", err)
	}
	return 0
}

func indexCmd(args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	dir := fs.String("dir", "./snapshots", "directory with ncdu snapshots")
	fs.Parse(args)
	st, err := store.Open(*dir, 1, log)
	if err != nil {
		log.Error("cannot open snapshot directory", "err", err)
		return 1
	}
	if err := st.Refresh(); err != nil {
		log.Error("cannot scan snapshot directory", "err", err)
		return 1
	}
	st.IngestAll()
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SNAPSHOT\tSTATUS\tROOT\tSIZE\tFILES\tNOTE")
	failed := 0
	for _, sn := range st.List() {
		root, size, files, note := "", "", "", sn.Error
		if s := sn.Summary; s != nil {
			root, size, files = s.Meta.Root, human(s.DSize), fmt.Sprint(s.Files)
		}
		if sn.Status == store.StatusError {
			failed++
		}
		for _, e := range sn.SidecarErrors {
			note += " sidecar: " + e
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", sn.ID, sn.Status, root, size, files, note)
	}
	tw.Flush()
	if failed > 0 {
		return 1
	}
	return 0
}

func human(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
