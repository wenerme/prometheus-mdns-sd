// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"sync"
	"time"

	"context"
	"github.com/prometheus/common/model"

	"github.com/hashicorp/mdns"
)

type TargetGroup struct {
	Targets []string          `json:"targets,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

const (
	dnsNameLabel = model.MetaLabelPrefix + "mdns_name"
)

func init() {
	// hashicorp/mdns outputs a lot of garbage on stdlog, so quiet it down...
	log.SetOutput(ioutil.Discard)
}

func main() {
	fmt.Println("main.start")
	d := &Discovery{
		interval: time.Duration(10 * time.Second),
	}

	ctx := context.Background()
	ch := make(chan []*TargetGroup)

	go d.Run(ctx, ch)

	func() {
		for targetList := range ch {
			y, _ := json.Marshal(targetList)
			fmt.Println("GOT TARGET LIST:\n", string(y))
		}
	}()
}

// Discovery periodically performs DNS-SD requests. It implements
// the TargetProvider interface.
type Discovery struct {
	names []string

	interval time.Duration
	m        sync.RWMutex
	port     int
	qtype    uint16
}

// Run implements the TargetProvider interface.
func (dd *Discovery) Run(ctx context.Context, ch chan<- []*TargetGroup) {
	fmt.Println("Discovery.run.start")
	defer close(ch)

	ticker := time.NewTicker(dd.interval)
	defer ticker.Stop()

	// Get an initial set right away.
	dd.refreshAll(ctx, ch)

	for {
		select {
		case <-ticker.C:
			dd.refreshAll(ctx, ch)
		case <-ctx.Done():
			return
		}
	}
}

func (dd *Discovery) refreshAll(ctx context.Context, ch chan<- []*TargetGroup) {
	var wg sync.WaitGroup

	names := []string{
		"_prometheus-http._tcp",
		//"_prometheus-https._tcp",
	}

	wg.Add(len(names))
	for _, name := range names {
		go func(n string) {
			if err := dd.refresh(ctx, n, ch); err != nil {
				//log.Errorf("Error refreshing DNS targets: %s", err)
			}
			wg.Done()
		}(name)
	}

	wg.Wait()
}

// TODO: Re-do so we select over ctx.Done(), a mdns response, mdns being done or an error
func (dd *Discovery) refresh(ctx context.Context, name string, ch chan<- []*TargetGroup) error {
	// Set up output channel and read discovered data
	responses := make(chan *mdns.ServiceEntry, 100)

	// Do the actual lookup
	go func() {
		// TODO: Capture err somewhere
		//err := mdns.Lookup(name, responses)
		mdns.Lookup(name, responses)
		close(responses)
	}()

	targetList := make([]*TargetGroup, 0)

	// Make a new targetGroup with one address-label for each thing we scape
	for response := range responses {
		labelSet := map[string]string{
			//dnsNameLabel:       model.LabelValue(name),
			model.MetaLabelPrefix + "mdns_name": response.Host,
			"instance":                          strings.TrimRight(response.Host, "."),
			"__scheme__":                        "http",
		}

		// Set model.SchemeLabel to 'http' or 'https'
		if strings.Contains(response.Name, "_prometheus-https._tcp") {
			labelSet["__scheme__"] = "https"
		}

		// Figure out an address
		addr := fmt.Sprintf("%s:%d", response.Host, response.Port)

		if response.AddrV4 != nil {
			addr = fmt.Sprintf("%s:%d", response.AddrV4, response.Port)
		} else if response.AddrV6 != nil {
			addr = fmt.Sprintf("[%s]:%d", response.AddrV6, response.Port)
		}

		tg := &TargetGroup{
			Labels:  labelSet,
			Targets: []string{addr},
		}

		fmt.Printf("now has TargetGroup %+v\n", tg)
		targetList = append(targetList, tg)

		// TODO: if HasTXT, parse InfoFields and set path as
		// model.MetricsPathLabel if it's there.
	}

	fmt.Printf("now has TargetGroupList %+v\n", targetList)

	// Fail...
	//if err != nil {
	//	return err
	//}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case ch <- targetList:
	}

	return nil
}
