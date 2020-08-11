package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	logging "github.com/ipfs/go-log"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/node/repo"
)

var log = logging.Logger("journal")

// fsJournal is a basic journal backed by files on a filesystem.
type fsJournal struct {
	EventTypeFactory

	dir       string
	sizeLimit int64

	lk    sync.Mutex
	fi    *os.File
	fSize int64

	incoming chan *Event

	closing chan struct{}
	closed  chan struct{}
}

// OpenFSJournal constructs a rolling filesystem journal, with a default
// per-file size limit of 1GiB.
func OpenFSJournal(lr repo.LockedRepo, disabled DisabledEvents) (Journal, error) {
	dir := filepath.Join(lr.Path(), "journal")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to mk directory %s for file journal: %w", dir, err)
	}

	f := &fsJournal{
		EventTypeFactory: NewEventTypeFactory(disabled),
		dir:              dir,
		sizeLimit:        1 << 30,
		incoming:         make(chan *Event, 32),
		closing:          make(chan struct{}),
		closed:           make(chan struct{}),
	}

	if err := f.rollJournalFile(); err != nil {
		return nil, err
	}

	go f.runLoop()

	return f, nil
}

func (f *fsJournal) RecordEvent(evtType EventType, obj interface{}) {
	je := &Event{
		EventType: evtType,
		Timestamp: build.Clock.Now(),
		Data:      obj,
	}
	select {
	case f.incoming <- je:
	case <-f.closing:
		log.Warnw("journal closed but tried to log event", "event", je)
	}
}

func (f *fsJournal) Close() error {
	close(f.closing)
	<-f.closed
	return nil
}

func (f *fsJournal) putEvent(evt *Event) error {
	b, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	n, err := f.fi.Write(append(b, '\n'))
	if err != nil {
		return err
	}

	f.fSize += int64(n)

	if f.fSize >= f.sizeLimit {
		_ = f.rollJournalFile()
	}

	return nil
}

func (f *fsJournal) rollJournalFile() error {
	if f.fi != nil {
		_ = f.fi.Close()
	}

	nfi, err := os.Create(filepath.Join(f.dir, fmt.Sprintf("lotus-journal-%s.ndjson", build.Clock.Now().Format(time.RFC3339))))
	if err != nil {
		return xerrors.Errorf("failed to open journal file: %w", err)
	}

	f.fi = nfi
	f.fSize = 0
	return nil
}

func (f *fsJournal) runLoop() {
	defer close(f.closed)

	for {
		select {
		case je := <-f.incoming:
			if err := f.putEvent(je); err != nil {
				log.Errorw("failed to write out journal event", "event", je, "err", err)
			}
		case <-f.closing:
			_ = f.fi.Close()
			return
		}
	}
}