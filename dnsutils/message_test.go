package dnsutils

import (
	"strings"
	"testing"
)

func TestDnsMessageToText(t *testing.T) {
	config := GetFakeConfig()

	dm := DnsMessage{}
	dm.Init()

	line := dm.String(strings.Fields(config.Subprocessors.TextFormat))

	if string(line) != "- - - - - - - - 0b - - -\n" {
		t.Errorf("text dns message invalid; %s", line)
	}
}
