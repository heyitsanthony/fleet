package systemd

import (
	"github.com/coreos/fleet/third_party/github.com/coreos/go-systemd/dbus"
	log "github.com/coreos/fleet/third_party/github.com/golang/glog"

	"github.com/coreos/fleet/event"
	"github.com/coreos/fleet/unit"
)

type EventStream struct {
	close chan bool
}

func NewEventStream() *EventStream {
	return &EventStream{make(chan bool)}
}

func (self *EventStream) Stream(unitchan <-chan map[string]*dbus.UnitStatus, eventchan chan *event.Event) {
	for true {
		select {
		case <-self.close:
			return
		case units := <-unitchan:
			log.V(1).Infof("Received event from dbus")
			events := translateUnitStatusEvents(units)
			for i, _ := range events {
				ev := events[i]
				log.V(1).Infof("Translated dbus event to event(Type=%s)", ev.Type)
				eventchan <- &ev
			}
		}
	}
}

func (self *EventStream) Close() {
	close(self.close)
}

func translateUnitStatusEvents(changes map[string]*dbus.UnitStatus) []event.Event {
	events := make([]event.Event, 0)
	for key, status := range changes {
		jobName := key
		var state *unit.UnitState
		if status != nil {
			state = unit.NewUnitState(status.LoadState, status.ActiveState, status.SubState, nil, nil)
		}
		ev := event.Event{"EventUnitStateUpdated", state, jobName}
		events = append(events, ev)
	}
	return events
}
