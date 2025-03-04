package collectors

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/dmachard/go-dnscollector/dnsutils"
	"github.com/dmachard/go-dnscollector/subprocessors"
	"github.com/dmachard/go-logger"
	"github.com/hpcloud/tail"
	"github.com/miekg/dns"
)

type Tail struct {
	done    chan bool
	tailf   *tail.Tail
	loggers []dnsutils.Worker
	config  *dnsutils.Config
	logger  *logger.Logger
}

func NewTail(loggers []dnsutils.Worker, config *dnsutils.Config, logger *logger.Logger) *Tail {
	s := &Tail{
		done:    make(chan bool),
		config:  config,
		loggers: loggers,
		logger:  logger,
	}
	s.ReadConfig()
	return s
}

func (c *Tail) Loggers() []chan dnsutils.DnsMessage {
	channels := []chan dnsutils.DnsMessage{}
	for _, p := range c.loggers {
		channels = append(channels, p.Channel())
	}
	return channels
}

func (c *Tail) ReadConfig() {
	//tbc
}

func (o *Tail) LogInfo(msg string, v ...interface{}) {
	o.logger.Info("collector tail - "+msg, v...)
}

func (o *Tail) LogError(msg string, v ...interface{}) {
	o.logger.Error("collector tail - "+msg, v...)
}

func (c *Tail) Channel() chan dnsutils.DnsMessage {
	return nil
}

func (c *Tail) Stop() {
	c.LogInfo("stopping...")

	// Stop to follow file
	c.LogInfo("stop following file...")
	c.tailf.Stop()

	// read done channel and block until run is terminated
	<-c.done
	close(c.done)
}

func (c *Tail) Follow() error {
	var err error
	location := tail.SeekInfo{Offset: 0, Whence: os.SEEK_END}
	config := tail.Config{Location: &location, ReOpen: true, Follow: true, Logger: tail.DiscardingLogger, Poll: true, MustExist: true}
	c.tailf, err = tail.TailFile(c.config.Collectors.Tail.FilePath, config)
	if err != nil {
		return err
	}
	return nil
}

func (c *Tail) Run() {
	c.LogInfo("starting collector...")
	err := c.Follow()
	if err != nil {
		c.logger.Fatal("collector tail - unable to follow file: ", err)
	}

	// geoip
	geoip := subprocessors.NewDnsGeoIpProcessor(c.config, c.logger)
	if err := geoip.Open(); err != nil {
		c.LogError("geoip init failed: %v+", err)
	}
	if geoip.IsEnabled() {
		c.LogInfo("geoip is enabled")
	}
	defer geoip.Close()

	// filtering
	filtering := subprocessors.NewFilteringProcessor(c.config, c.logger)

	// user privacy
	ipPrivacy := subprocessors.NewIpAnonymizerSubprocessor(c.config)
	qnamePrivacy := subprocessors.NewQnameReducerSubprocessor(c.config)

	dm := dnsutils.DnsMessage{}
	dm.Init()
	dm.DnsTap.Identity = c.config.Subprocessors.ServerId

	for line := range c.tailf.Lines {
		var matches []string
		var re *regexp.Regexp

		if len(c.config.Collectors.Tail.PatternQuery) > 0 {
			re = regexp.MustCompile(c.config.Collectors.Tail.PatternQuery)
			matches = re.FindStringSubmatch(line.Text)
			dm.DNS.Type = dnsutils.DnsQuery
			dm.DnsTap.Operation = "QUERY"
		}

		if len(c.config.Collectors.Tail.PatternReply) > 0 && len(matches) == 0 {
			re = regexp.MustCompile(c.config.Collectors.Tail.PatternReply)
			matches = re.FindStringSubmatch(line.Text)
			dm.DNS.Type = dnsutils.DnsReply
			dm.DnsTap.Operation = "REPLY"
		}

		if len(matches) == 0 {
			continue
		}

		qrIndex := re.SubexpIndex("qr")
		if qrIndex != -1 {
			dm.DnsTap.Operation = matches[qrIndex]
		}

		var t time.Time
		timestampIndex := re.SubexpIndex("timestamp")
		if timestampIndex != -1 {
			t, err = time.Parse(c.config.Collectors.Tail.TimeLayout, matches[timestampIndex])
			if err != nil {
				continue
			}
		} else {
			t = time.Now()
		}
		dm.DnsTap.TimeSec = int(t.Unix())
		dm.DnsTap.TimeNsec = int(t.UnixNano() - t.Unix()*1e9)

		identityIndex := re.SubexpIndex("identity")
		if identityIndex != -1 {
			dm.DnsTap.Identity = matches[identityIndex]
		}

		rcodeIndex := re.SubexpIndex("rcode")
		if rcodeIndex != -1 {
			dm.DNS.Rcode = matches[rcodeIndex]
		}

		queryipIndex := re.SubexpIndex("queryip")
		if queryipIndex != -1 {
			dm.NetworkInfo.QueryIp = matches[queryipIndex]
		}

		queryportIndex := re.SubexpIndex("queryport")
		if queryportIndex != -1 {
			dm.NetworkInfo.QueryPort = matches[queryportIndex]
		}

		responseipIndex := re.SubexpIndex("responseip")
		if responseipIndex != -1 {
			dm.NetworkInfo.ResponseIp = matches[responseipIndex]
		}

		responseportIndex := re.SubexpIndex("responseport")
		if responseportIndex != -1 {
			dm.NetworkInfo.ResponsePort = matches[responseportIndex]
		}

		familyIndex := re.SubexpIndex("family")
		if familyIndex != -1 {
			dm.NetworkInfo.Family = matches[familyIndex]
		} else {
			dm.NetworkInfo.Family = "INET"
		}

		protocolIndex := re.SubexpIndex("protocol")
		if protocolIndex != -1 {
			dm.NetworkInfo.Protocol = matches[protocolIndex]
		} else {
			dm.NetworkInfo.Protocol = "UDP"
		}

		lengthIndex := re.SubexpIndex("length")
		if lengthIndex != -1 {
			length, err := strconv.Atoi(matches[lengthIndex])
			if err == nil {
				dm.DNS.Length = length
			}
		}

		domainIndex := re.SubexpIndex("domain")
		if domainIndex != -1 {
			dm.DNS.Qname = matches[domainIndex]
		}

		qtypeIndex := re.SubexpIndex("qtype")
		if qtypeIndex != -1 {
			dm.DNS.Qtype = matches[qtypeIndex]
		}

		latencyIndex := re.SubexpIndex("latency")
		if latencyIndex != -1 {
			dm.DnsTap.LatencySec = matches[latencyIndex]
		}

		// compute timestamp
		dm.DnsTap.Timestamp = float64(dm.DnsTap.TimeSec) + float64(dm.DnsTap.TimeNsec)/1e9
		ts := time.Unix(int64(dm.DnsTap.TimeSec), int64(dm.DnsTap.TimeNsec))
		dm.DnsTap.TimestampRFC3339 = ts.UTC().Format(time.RFC3339Nano)

		// fake dns packet
		dnspkt := new(dns.Msg)
		var dnstype uint16
		dnstype = dns.TypeA
		if dm.DNS.Qtype == "AAAA" {
			dnstype = dns.TypeAAAA
		}
		dnspkt.SetQuestion(dm.DNS.Qname, dnstype)

		if dm.DNS.Type == dnsutils.DnsReply {
			rr, _ := dns.NewRR(fmt.Sprintf("%s %s 0.0.0.0", dm.DNS.Qname, dm.DNS.Qtype))
			if err == nil {
				dnspkt.Answer = append(dnspkt.Answer, rr)
			}
			var rcode int
			rcode = 0
			if dm.DNS.Rcode == "NXDOMAIN" {
				rcode = 3
			}
			dnspkt.Rcode = rcode
		}

		dm.DNS.Payload, _ = dnspkt.Pack()
		dm.DNS.Length = len(dm.DNS.Payload)

		// qname privacy
		if qnamePrivacy.IsEnabled() {
			dm.DNS.Qname = qnamePrivacy.Minimaze(dm.DNS.Qname)
		}

		// filtering
		if filtering.CheckIfDrop(&dm) {
			continue
		}

		// geoip feature
		if geoip.IsEnabled() {
			geoInfo, err := geoip.Lookup(dm.NetworkInfo.QueryIp)
			if err != nil {
				c.LogError("geoip loopkup failed: %v+", err)
			}
			dm.Geo.Continent = geoInfo.Continent
			dm.Geo.CountryIsoCode = geoInfo.CountryISOCode
			dm.Geo.City = geoInfo.City
			dm.NetworkInfo.AutonomousSystemNumber = geoInfo.ASN
			dm.NetworkInfo.AutonomousSystemOrg = geoInfo.ASO
		}

		// ip anonymisation ?
		if ipPrivacy.IsEnabled() {
			dm.NetworkInfo.QueryIp = ipPrivacy.Anonymize(dm.NetworkInfo.QueryIp)
		}

		// send to loggers
		chanLoggers := c.Loggers()
		for i := range chanLoggers {
			chanLoggers[i] <- dm
		}
	}

	c.LogInfo("run terminated")
	c.done <- true
}
