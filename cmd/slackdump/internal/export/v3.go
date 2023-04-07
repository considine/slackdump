package export

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/rusq/dlog"
	"github.com/rusq/fsadapter"
	"github.com/rusq/slackdump/v2"
	"github.com/rusq/slackdump/v2/cmd/slackdump/internal/export/expproc"
	"github.com/rusq/slackdump/v2/export"
	"github.com/rusq/slackdump/v2/internal/structures"
	"github.com/schollz/progressbar/v3"
	"github.com/slack-go/slack"
)

func exportV3(ctx context.Context, sess *slackdump.Session, fs fsadapter.FS, list *structures.EntityList, options export.Config) error {
	lg := dlog.FromContext(ctx)
	tmpdir, err := os.MkdirTemp("", "slackdump-*")
	if err != nil {
		return err
	}
	lg.Printf("using %s as the temporary directory", tmpdir)
	lg.Print("running export...")

	errC := make(chan error, 1)
	s := sess.Stream()
	var wg sync.WaitGroup

	// Generator of channel IDs.
	links := make(chan string)
	{
		wg.Add(1)
		var generator linkFeederFunc
		if list.HasIncludes() {
			// inclusive export, processes only included channels.
			generator = listChannelGenerator
		} else {
			// exclusive export (process only excludes, if any)
			generator = apiChannelGenerator(tmpdir, s, options.MemberOnly)
		}

		go func() {
			defer wg.Done()
			defer close(links)
			errC <- generator(ctx, links, list) // TODO
			lg.Debug("channels done")
		}()
	}
	// user goroutine
	{
		wg.Add(1)
		go func() {
			defer wg.Done()
			errC <- userWorker(ctx, s, tmpdir)
		}()
	}
	// conversations goroutine
	{
		pb := newProgressBar(progressbar.NewOptions(-1, progressbar.OptionClearOnFinish(), progressbar.OptionSpinnerType(8)), lg.IsDebug())
		pb.RenderBlank()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer pb.Finish()
			errC <- conversationWorker(ctx, s, pb, tmpdir, links)

		}()
	}
	// sentinel
	go func() {
		wg.Wait()
		close(errC)
	}()

	// process returned errors
	for err := range errC {
		if err != nil {
			return err
		}
	}
	lg.Printf("conversations export finished, chunk files in: %s", tmpdir)
	return nil
}

type linkFeederFunc func(ctx context.Context, links chan<- string, list *structures.EntityList) error

// listChannelGenerator feeds the channel IDs that it gets from the list to
// the links channel.  It does not fetch the channel list from the api, so
// it's blazing fast in comparison to apiChannelFeeder.  When needed, get the
// channel information from the conversations chunk files (they contain the
// chunk with channel information).
func listChannelGenerator(ctx context.Context, links chan<- string, list *structures.EntityList) error {
	for _, ch := range list.Include {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case links <- ch:
		}
	}
	return nil
}

// apiChannelGenerator feeds the channel IDs that it gets from the API to the
// links channel.  It also filters out channels that are excluded in the list.
// It does not account for "included".  It ignores the thread links in the
// list.  It writes the channels to the tmpdir.
func apiChannelGenerator(tmpdir string, s *slackdump.Stream, memberOnly bool) linkFeederFunc {
	return linkFeederFunc(func(ctx context.Context, links chan<- string, list *structures.EntityList) error {
		chIdx := list.Index()
		chanproc, err := expproc.NewChannels(tmpdir, func(c []slack.Channel) error {
			for _, ch := range c {
				if memberOnly && !ch.IsMember {
					continue
				}
				if include, ok := chIdx[ch.ID]; ok && !include {
					continue
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case links <- ch.ID:

				}
			}
			return nil
		})
		if err != nil {
			return err
		}

		if err := s.ListChannels(ctx, chanproc, &slack.GetConversationsParameters{Types: slackdump.AllChanTypes}); err != nil {
			return fmt.Errorf("error listing channels: %w", err)
		}
		if err := chanproc.Close(); err != nil {
			return fmt.Errorf("error closing channel processor: %w", err)
		}
		dlog.FromContext(ctx).Debug("channels done")
		return nil
	})
}

func userWorker(ctx context.Context, s *slackdump.Stream, tmpdir string) error {
	userproc, err := expproc.NewUsers(tmpdir)
	if err != nil {
		return err
	}

	if err := s.Users(ctx, userproc); err != nil {
		return fmt.Errorf("error listing users: %w", err)
	}
	if err := userproc.Close(); err != nil {
		return fmt.Errorf("error closing user processor: %w", err)
	}
	dlog.FromContext(ctx).Debug("users done")
	return nil
}

// progresser is an interface for progress bars.
type progresser interface {
	RenderBlank() error
	Describe(description string)
	Add(num int) error
	Finish() error
}

func conversationWorker(ctx context.Context, s *slackdump.Stream, pb progresser, tmpdir string, links <-chan string) error {
	conv, err := expproc.NewConversation(tmpdir)
	if err != nil {
		return fmt.Errorf("error initialising conversation processor: %w", err)
	}

	if err := s.AsyncConversations(ctx, conv, links, func(sr slackdump.StreamResult) error {
		pb.Describe(sr.String())
		pb.Add(1)
		return nil
	}); err != nil {
		return fmt.Errorf("error streaming conversations: %w", err)
	}
	dlog.FromContext(ctx).Debug("conversations done")
	pb.Describe("OK")
	return nil
}

func newProgressBar(pb *progressbar.ProgressBar, debug bool) progresser {
	if debug {
		return progressbar.DefaultSilent(0)
	}
	return pb
}
