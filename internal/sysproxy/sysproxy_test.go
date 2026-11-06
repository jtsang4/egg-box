package sysproxy

import (
	"context"
	"net"
	"reflect"
	"strconv"
	"testing"
)

// listeningOn answers from a fixed set of "host:port".
func listeningOn(addrs ...string) Listening {
	set := map[string]bool{}
	for _, a := range addrs {
		set[a] = true
	}
	return func(_ context.Context, host string, port int) bool {
		return set[net.JoinHostPort(host, strconv.Itoa(port))]
	}
}

func TestAnalyzeGroupsLocalAddressesAndJudgesThem(t *testing.T) {
	settings := []Setting{
		{Service: "Wi-Fi", Active: true, Kind: KindHTTP, Host: "127.0.0.1", Port: 3067},
		{Service: "Wi-Fi", Active: true, Kind: KindHTTPS, Host: "127.0.0.1", Port: 3067},
		{Service: "Wi-Fi", Active: true, Kind: KindSOCKS, Host: "127.0.0.1", Port: 3067},
		{Service: "USB LAN", Kind: KindHTTP, Host: "127.0.0.1", Port: 7890},
		{Service: "iPhone USB", Kind: KindHTTP, Host: "localhost", Port: 7891},
		// Another machine's proxy is somebody's choice: never judged.
		{Service: "Ethernet", Kind: KindHTTP, Host: "10.0.0.8", Port: 3128},
		{Service: "Ethernet", Kind: KindHTTP, Host: "proxy.corp.example", Port: 8080},
	}
	got := Analyze(context.Background(), settings, nil, listeningOn("localhost:7891"))
	if len(got) != 3 {
		t.Fatalf("want 3 issues, got %+v", got)
	}
	first := got[0]
	if first.Endpoint() != "127.0.0.1:3067" || first.State != Dead || !first.Active || len(first.Settings) != 3 {
		t.Fatalf("the active dead address comes first with its three settings, got %+v", first)
	}
	if got[1].Endpoint() != "127.0.0.1:7890" || got[1].State != Dead || got[1].Active {
		t.Fatalf("an inactive dead address comes next, got %+v", got[1])
	}
	if got[2].Endpoint() != "localhost:7891" || got[2].State != Foreign {
		t.Fatalf("a port something listens on belongs to another program, got %+v", got[2])
	}
}

func TestAnalyzeLeavesTheApplicationsOwnPortAlone(t *testing.T) {
	settings := []Setting{{Service: "Wi-Fi", Active: true, Kind: KindHTTP, Host: "127.0.0.1", Port: 7890}}
	if got := Analyze(context.Background(), settings, []int{7890}, listeningOn()); len(got) != 0 {
		t.Fatalf("a setting pointing at our own proxy is the person's choice, got %+v", got)
	}
}

func TestAnalyzeRecognisesLoopbackSpellings(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "127.1.2.3", "::1", "[::1]", "LOCALHOST", " 127.0.0.1 ", "::ffff:127.0.0.1"} {
		got := Analyze(context.Background(), []Setting{{Service: "s", Kind: KindHTTP, Host: h, Port: 1}}, nil, listeningOn())
		if len(got) != 1 {
			t.Errorf("%q is this machine", h)
		}
	}
	for _, h := range []string{"192.168.1.1", "0.0.0.0", "local.example", ""} {
		got := Analyze(context.Background(), []Setting{{Service: "s", Kind: KindHTTP, Host: h, Port: 1}}, nil, listeningOn())
		if len(got) != 0 {
			t.Errorf("%q is not judged", h)
		}
	}
}

func TestServicesListsTheActiveOneFirstOnce(t *testing.T) {
	is := Issue{Settings: []Setting{
		{Service: "USB LAN"}, {Service: "Wi-Fi", Active: true}, {Service: "USB LAN"}, {Service: "Wi-Fi", Active: true},
	}}
	if got := is.Services(); !reflect.DeepEqual(got, []string{"Wi-Fi", "USB LAN"}) {
		t.Fatalf("got %v", got)
	}
}

func TestParseDarwin(t *testing.T) {
	report := `{"primary":"A","services":[
		{"id":"A","name":"Wi-Fi","proxies":{"HTTPEnable":1,"HTTPProxy":"127.0.0.1","HTTPPort":3067,
			"HTTPSEnable":1,"HTTPSProxy":"127.0.0.1","HTTPSPort":3067,"SOCKSEnable":0,"SOCKSProxy":"127.0.0.1","SOCKSPort":3067,
			"ExceptionsList":["*.local"]}},
		{"id":"B","name":"USB 10/100/1000 LAN","proxies":{"SOCKSEnable":1,"SOCKSProxy":"127.0.0.1","SOCKSPort":7890}},
		{"id":"C","name":"No port","proxies":{"HTTPEnable":1,"HTTPProxy":"127.0.0.1"}},
		{"id":"D","name":"Empty","proxies":{}}
	]}`
	got, err := ParseDarwin([]byte(report))
	if err != nil {
		t.Fatal(err)
	}
	want := []Setting{
		{Service: "Wi-Fi", Active: true, Kind: KindHTTP, Host: "127.0.0.1", Port: 3067},
		{Service: "Wi-Fi", Active: true, Kind: KindHTTPS, Host: "127.0.0.1", Port: 3067},
		{Service: "USB 10/100/1000 LAN", Kind: KindSOCKS, Host: "127.0.0.1", Port: 7890},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if _, err := ParseDarwin([]byte("not json")); err == nil {
		t.Fatal("a broken report is an error")
	}
}

func TestDarwinCommandsSwitchEachSettingOffOnce(t *testing.T) {
	issues := []Issue{
		{Settings: []Setting{
			{Service: "Wi-Fi", Kind: KindHTTP}, {Service: "Wi-Fi", Kind: KindHTTPS}, {Service: "Wi-Fi", Kind: KindSOCKS},
		}},
		{Settings: []Setting{{Service: "Bob's USB 10/100", Kind: KindHTTP}, {Service: "Wi-Fi", Kind: KindHTTP}}},
	}
	want := "networksetup -setwebproxystate 'Wi-Fi' off\n" +
		"networksetup -setsecurewebproxystate 'Wi-Fi' off\n" +
		"networksetup -setsocksfirewallproxystate 'Wi-Fi' off\n" +
		"networksetup -setwebproxystate 'Bob'\\''s USB 10/100' off\n"
	if got := DarwinCommands(issues); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if !Probe(context.Background(), "127.0.0.1", port) {
		t.Fatal("a listening port is found")
	}
	_ = ln.Close()
	if Probe(context.Background(), "127.0.0.1", port) {
		t.Fatal("a closed port is not")
	}
}
