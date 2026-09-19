package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/ginkio/addigyctl/internal/addigy"
)

func (ds *deviceServer) runGet(t *testing.T, cmd *DevicesGetCmd) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app := &App{
		Ctx: context.Background(),
		G:   &Globals{APIKey: "k", BaseURL: ds.URL + "/api/v2"},
		Out: &out,
		Err: io.Discard,
	}
	err := cmd.Run(app)
	return out.String(), err
}

// The fake server ignores the search text and returns every device, so these
// tests exercise how the CLI picks the right one.

func TestDevicesGetByAgentIDAndSerial(t *testing.T) {
	ds := newDeviceServer(t)
	for ref, want := range map[string]string{"d3": "d3", "D-4": "d4", "b-2": "d1"} {
		out, err := ds.runGet(t, &DevicesGetCmd{Ref: ref})
		if err != nil {
			t.Fatalf("%q: %v", ref, err)
		}
		if !strings.Contains(out, "Agent ID:    "+want+"\n") {
			t.Errorf("%q should select %s:\n%s", ref, want, out)
		}
	}
}

func TestDevicesGetWithAPolicyIDExplainsTheMistake(t *testing.T) {
	ds := newDeviceServer(t)
	// "acme" is the policy_id fact of d1, but it is a policy, not a device.
	_, err := ds.runGet(t, &DevicesGetCmd{Ref: "acme"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		`"acme" matches`,
		"7 devices",
		`"Acme" is a policy (acme), not a device`,
		"addigyctl devices list --policy acme",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

func TestPolicyIDDoesNotIdentifyADevice(t *testing.T) {
	d1 := addigy.Device{AgentID: "d1", Facts: map[string]addigy.Fact{
		"serial_number": {Value: "B-2"},
		"policy_id":     {Value: "policy-x"},
	}}
	if identifiesDevice(d1, "policy-x") {
		t.Error("the location must not identify a device")
	}
	for _, ref := range []string{"d1", "D1", "b-2"} {
		if !identifiesDevice(d1, ref) {
			t.Errorf("%q should identify the device", ref)
		}
	}
}

func devices(n int) []addigy.Device {
	ds := make([]addigy.Device, n)
	for i := range ds {
		ds[i] = addigy.Device{AgentID: fmt.Sprintf("agent-%02d", i), Facts: map[string]addigy.Fact{
			"serial_number": {Value: fmt.Sprintf("SN%02d", i)},
			"device_name":   {Value: fmt.Sprintf("Mac %02d", i)},
		}}
	}
	return ds
}

func TestPickDeviceAmbiguityErrorIsBounded(t *testing.T) {
	_, err := pickDevice(devices(25), "no-such-device", 118)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"118 devices", "First 10:", "agent-00  SN00  Mac 00", "agent-09", "… and 108 more"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "agent-10") {
		t.Errorf("only the first 10 candidates should be listed:\n%s", msg)
	}
}

func TestPickDeviceSingleLooseMatchAndNoMatch(t *testing.T) {
	one := devices(1)
	if d, err := pickDevice(one, "mac", 1); err != nil || d.AgentID != "agent-00" {
		t.Errorf("a single search result should be used: %v %v", d.AgentID, err)
	}
	if _, err := pickDevice(nil, "nope", 0); err == nil || !strings.Contains(err.Error(), "no device matches") {
		t.Errorf("expected a no-match error, got %v", err)
	}
}

func TestPickDeviceExactMatchAmongMany(t *testing.T) {
	d, err := pickDevice(devices(25), "sn07", 25)
	if err != nil || d.AgentID != "agent-07" {
		t.Errorf("got %v, %v", d.AgentID, err)
	}
}
