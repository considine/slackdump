package processors

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/slack-go/slack"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrExhausted = errors.New("exhausted")
)

// Player replays the events from a file, it is able to emulate the API
// responses, if used in conjunction with the [proctest.Server]. Zero value is
// not usable.
type Player struct {
	rs io.ReadSeeker

	pointer state // current event pointers

	idx index
}

// index holds the index of each event type within the file.  key is the event
// ID, value is the list of offsets for that id in the file.
type index map[string][]int64

// state holds the index of the current offset for each event id.
type state map[string]int

// NewPlayer creates a new event player from the io.ReadSeeker.
func NewPlayer(rs io.ReadSeeker) (*Player, error) {
	idx, err := indexRecords(rs)
	if err != nil {
		return nil, err
	}
	return &Player{
		rs:      rs,
		idx:     idx,
		pointer: make(state),
	}, nil
}

// indexRecords indexes the records in the reader and returns an index.
func indexRecords(rs io.ReadSeeker) (index, error) {
	var idx = make(index)

	dec := json.NewDecoder(rs)

	for i := 0; ; i++ {
		offset := dec.InputOffset() // record current offset

		var event Event
		if err := dec.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		idx[event.ID()] = append(idx[event.ID()], offset)
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil { // reset offset
		return nil, err
	}
	return idx, nil
}

func (p *Player) tryGetEvent(id string) (*Event, error) {
	offsets, ok := p.idx[id]
	if !ok {
		return nil, ErrNotFound
	}
	// getting current offset index for the requested id.
	ptr, ok := p.pointer[id]
	if !ok {
		p.pointer[id] = 0 // initialize, if we see it the first time.
	}
	if ptr >= len(offsets) { // check if we've exhausted the messages
		return nil, io.EOF
	}

	_, err := p.rs.Seek(offsets[ptr], io.SeekStart) // seek to the offset
	if err != nil {
		return nil, err
	}
	var event Event
	if err := json.NewDecoder(p.rs).Decode(&event); err != nil {
		return nil, err
	}
	p.pointer[id]++ // increase the offset pointer for the next call.
	return &event, nil
}

func (p *Player) hasMore(id string) bool {
	offsets, ok := p.idx[id]
	if !ok {
		return false
	}
	// getting current offset index for the requested id.
	ptr, ok := p.pointer[id]
	if !ok {
		p.pointer[id] = 0 // initialize, if we see it the first time.
	}
	return ptr < len(offsets)
}

func (p *Player) Messages(channelID string) ([]slack.Message, error) {
	event, err := p.tryGetEvent(channelID)
	if err != nil {
		return nil, err
	}
	return event.Messages, nil
}

// HasMoreMessages returns true if there are more messages to be read for the
// channel.
func (p *Player) HasMoreMessages(channelID string) bool {
	return p.hasMore(channelID)
}

func (p *Player) Thread(channelID string, threadTS string) ([]slack.Message, error) {
	id := threadID(channelID, threadTS)
	event, err := p.tryGetEvent(id)
	if err != nil {
		return nil, err
	}
	return event.Messages, nil
}

func (p *Player) HasMoreThreads(channelID string, threadTS string) bool {
	return p.hasMore(threadID(channelID, threadTS))
}

func (p *Player) Reset() error {
	p.pointer = make(state)
	_, err := p.rs.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	return nil
}

// Replay replays the events in the reader to the channeler in the order they
// were recorded.  It will reset the state of the Player.
func (p *Player) Replay(c Channeler) error {
	if err := p.Reset(); err != nil {
		return err
	}
	defer p.rs.Seek(0, io.SeekStart) // reset offset once we finished.
	dec := json.NewDecoder(p.rs)
	for {
		var evt Event
		if err := dec.Decode(&evt); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if err := p.emit(c, evt); err != nil {
			return err
		}
	}
	return nil
}

// emit emits the event to the channeler.
func (p *Player) emit(c Channeler, evt Event) error {
	switch evt.Type {
	case EventMessages:
		if err := c.Messages(evt.ChannelID, evt.Messages); err != nil {
			return err
		}
	case EventThreadMessages:
		if err := c.ThreadMessages(evt.ChannelID, *evt.Parent, evt.Messages); err != nil {
			return err
		}
	case EventFiles:
		if err := c.Files(evt.ChannelID, *evt.Parent, evt.IsThreadMessage, evt.Files); err != nil {
			return err
		}
	}
	return nil
}
