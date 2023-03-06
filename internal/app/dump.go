package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/trace"
	"time"

	"github.com/rusq/fsadapter"
	"github.com/rusq/slackdump/v2"
	"github.com/rusq/slackdump/v2/auth"
	"github.com/rusq/slackdump/v2/internal/app/config"
	"github.com/rusq/slackdump/v2/internal/format"
	"github.com/rusq/slackdump/v2/internal/nametmpl"
	"github.com/rusq/slackdump/v2/logger"
	"github.com/rusq/slackdump/v2/types"
)

type dump struct {
	sess *slackdump.Session
	cfg  config.Params

	log logger.Interface
}

func Dump(ctx context.Context, cfg config.Params, prov auth.Provider) error {
	ctx, task := trace.NewTask(ctx, "runDump")
	defer task.End()

	cfg.SlackConfig.BaseLocation = cfg.Output.Base

	dm, err := newDump(ctx, cfg, prov)
	if err != nil {
		return err
	}
	defer dm.Close()

	if cfg.ListFlags.FlagsPresent() {
		err = dm.List(ctx)
	} else {
		var n int
		n, err = dm.Dump(ctx)
		cfg.Logger().Printf("dumped %d item(s)", n)
	}

	return err
}

func newDump(ctx context.Context, cfg config.Params, prov auth.Provider) (*dump, error) {
	sess, err := slackdump.New(ctx, prov, cfg.SlackConfig)
	if err != nil {
		return nil, err
	}

	return &dump{sess: sess, cfg: cfg, log: cfg.Logger()}, nil
}

func (d *dump) Close() error {
	return d.sess.Close()
}

// Dump dumps the input, if dumpfiles is true, it will save the files into a
// respective directory with ID of the channel as the name.  If generateText is
// true, it will additionally format the conversation as text file and write it
// to <ID>.txt file.
//
// The result of the work of this function, for each channel ID, the following
// files will be created:
//
//	+-<ID> - directory, if dumpfiles is true
//	|  +- attachment1.ext
//	|  +- attachment2.ext
//	|  +- ...
//	+--<ID>.json - json file with conversation and users
//	+--<ID>.txt  - formatted conversation in text format, if generateText is true.
func (app *dump) Dump(ctx context.Context) (int, error) {
	if !app.cfg.Input.IsValid() {
		return 0, errors.New("no valid input")
	}

	tmpl, err := app.cfg.CompileTemplates()
	if err != nil {
		return 0, err
	}

	total := 0
	if err := app.cfg.Input.Producer(func(channelID string) error {
		if err := app.dumpOne(ctx, app.sess.Filesystem(), tmpl, channelID, app.sess.Dump); err != nil {
			app.log.Printf("error processing: %q (conversation will be skipped): %s", channelID, err)
			return config.ErrSkip
		}
		total++
		return nil
	}); err != nil {
		return total, err
	}
	return total, nil
}

type dumpFunc func(context.Context, string, time.Time, time.Time, ...slackdump.ProcessFunc) (*types.Conversation, error)

// dumpOneChannel dumps just one channel specified by channelInput.  If
// generateText is true, it will also generate a ID.txt text file.
func (app *dump) dumpOne(ctx context.Context, fs fsadapter.FS, filetmpl *nametmpl.Template, channelInput string, fn dumpFunc) error {
	cnv, err := fn(ctx, channelInput, time.Time(app.cfg.Oldest), time.Time(app.cfg.Latest))
	if err != nil {
		return err
	}

	filename, err := filetmpl.Execute(cnv)
	if err != nil {
		return err
	}
	return app.writeFiles(ctx, fs, filename, cnv)
}

// writeFiles writes the conversation to disk.  If text output is set, it will
// also generate a text file having the same name as JSON file.
func (app *dump) writeFiles(ctx context.Context, fs fsadapter.FS, name string, cnv *types.Conversation) error {
	if err := app.writeJSON(fs, name+".json", cnv); err != nil {
		return err
	}
	if app.cfg.Output.IsText() {
		if err := app.writeText(ctx, fs, name+".txt", cnv); err != nil {
			return err
		}
	}
	return nil
}

func (app *dump) writeJSON(fs fsadapter.FS, filename string, m any) error {
	f, err := fs.Create(filename)
	if err != nil {
		return fmt.Errorf("error writing %q: %w", filename, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("error encoding %q: %w", filename, err)
	}
	return nil
}

func (app *dump) writeText(ctx context.Context, fs fsadapter.FS, filename string, m *types.Conversation) error {
	app.log.Printf("generating %s", filename)
	f, err := fs.Create(filename)
	if err != nil {
		return fmt.Errorf("error writing %q: %w", filename, err)
	}
	defer f.Close()
	txt := format.NewText()

	return txt.Conversation(ctx, f, app.sess.Users, m)
}

// List lists the supported entities, and writes the output to the output
// defined in the app.cfg.
func (app *dump) List(ctx context.Context) error {
	f, err := createFile(app.cfg.Output.Filename)
	if err != nil {
		return err
	}
	defer f.Close()

	app.log.Print("retrieving data...")

	var formatter format.Converter = format.NewJSON()
	if app.cfg.Output.IsText() {
		formatter = format.NewText()
	}

	switch {
	case app.cfg.ListFlags.Channels:
		ch, err := app.sess.GetChannels(ctx)
		if err != nil {
			return err
		}
		return formatter.Channels(ctx, f, app.sess.Users, ch)
	case app.cfg.ListFlags.Users:
		u, err := app.sess.GetUsers(ctx)
		if err != nil {
			return err
		}
		return formatter.Users(ctx, f, u)
	default:
		return errors.New("no valid list flag")
	}
}

// createFile creates the file, or opens the Stdout, if the filename is "-".
// It will return an error, if things go pear-shaped.
func createFile(filename string) (f io.WriteCloser, err error) {
	if filename == "-" {
		f = os.Stdout
		return
	}
	return os.Create(filename)
}
