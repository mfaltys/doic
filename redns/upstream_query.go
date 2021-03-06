package main

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/unixvoid/glogger"
	"gopkg.in/redis.v5"
)

func upstreamQuery(w dns.ResponseWriter, req *dns.Msg, redisClient *redis.Client) *dns.Msg {
	transport := "udp"
	if _, ok := w.RemoteAddr().(*net.TCPAddr); ok {
		transport = "tcp"
	}
	c := &dns.Client{Net: transport}
	resp, _, err := c.Exchange(req, config.Redns.UpstreamDNS)

	if err != nil {
		glogger.Debug.Println(err)
		dns.HandleFailed(w, req)
	}
	return resp
}

func anamepreresolve(w dns.ResponseWriter, req *dns.Msg, redisClient *redis.Client) {
	// parse hostname
	hostname := req.Question[0].Name

	if hostname == "localhost." {
		req = upstreamQuery(w, req, redisClient)
		// write response back from client
		if req != nil {
			w.WriteMsg(req)
		} else {
			glogger.Error.Println("Error getting response from upstream")
		}
	} else {
		anameresolve(w, req, redisClient)
	}
}

func anameresolve(w dns.ResponseWriter, req *dns.Msg, redisClient *redis.Client) {
	// parse hostname
	hostname := req.Question[0].Name

	// parse client ip
	client := strings.Split(w.RemoteAddr().String(), ":")

	// generate timestamp
	t := time.Now()
	timestamp := fmt.Sprintf("%s", t.Format("2006/01/02 15:04:05"))

	// add client to redis client:list
	err := redisClient.SAdd("client:list", client[0]).Err()
	if err != nil {
		glogger.Error.Printf("error adding client: '%s' to 'client:list'", client[0])
	}

	// add client history entry to redis
	err = redisClient.RPush(fmt.Sprintf("client:%s", client[0]), fmt.Sprintf("%s :: %s", timestamp, hostname)).Err()
	if err != nil {
		glogger.Error.Printf("error adding hostname: '%s' for client: 'client:%s'\n", hostname, client[0])
		glogger.Error.Printf("%s", err)
	}

	// un-fully qualify domain if its qualified
	bhost := hostname[:len(hostname)-1]

	// if wildcardsubdomain is set, test for the base domain
	if config.Redns.WildcardSubdomain {
		// parse out subdomain to leave base domain
		// this checks for site-wide entry/covers whole dom
		doms := strings.Split(bhost, ".")

		// its a subdomain if there are more than 2 splits
		if len(doms) > 2 {
			// set bhost to the parent domain
			ogbhost := bhost
			bhost = fmt.Sprintf("%s.%s", doms[len(doms)-2], doms[len(doms)-1])
			glogger.Debug.Printf("testing parent domain '%s' of '%s'", bhost, ogbhost)
		}
	}

	// query redis to see if entry exists in 'blacklist:domain' O(1)
	exists, err := redisClient.SIsMember("blacklist:domain", bhost).Result()
	if err != nil {
		glogger.Error.Println("error getting result from blacklist:domain")
		glogger.Error.Println(err)
	}

	// set client to 'localhost' if it comes from localhost
	if client[0] == "[" {
		client[0] = "localhost"
	}

	// handle blacklisted domain case
	if exists {
		glogger.Debug.Printf("intercepted blacklisted domain '%s' on client '%s'\n", hostname, client[0])
		externalIp := getoutboundIP()

		// return rcode3 to client (nonexist)
		rr := new(dns.A)
		rr.Hdr = dns.RR_Header{Name: hostname, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 1}

		// if redns is configured to use redirection give the configured/default redirect ip
		if config.Redirect.UseRedirect {
			// set the ip to the external ip if 'redirectsite' is not set
			if config.Redirect.RedirectSite == "" {
				rr.A = net.ParseIP(externalIp)
			} else {
				rr.A = net.ParseIP(config.Redirect.RedirectSite)
			}
		} else {
			rr.A = net.ParseIP("127.0.0.1")
		}

		// craft reply
		rep := new(dns.Msg)
		rep.SetReply(req)
		rep.SetRcode(req, dns.RcodeSuccess)
		//rep.SetRcode(req, dns.RcodeNameError)
		rep.Answer = append(rep.Answer, rr)

		// send it
		w.WriteMsg(rep)
	} else {
		// send request upstream
		glogger.Debug.Printf("client: %s\n", client[0])
		glogger.Debug.Printf("sending 'A' request for '%s' upstream\n", hostname)

		req = upstreamQuery(w, req, redisClient)
		// write response back from client
		if req != nil {
			w.WriteMsg(req)
		} else {
			glogger.Error.Println("Error getting response from upstream")
		}
	}
}

func aaaanameresolve(w dns.ResponseWriter, req *dns.Msg, redisClient *redis.Client) {
	// parse hostname
	hostname := req.Question[0].Name

	// return NOERROR and empty body to client (no ipv6 address exists, try ipv4)
	rr := new(dns.AAAA)
	rr.Hdr = dns.RR_Header{Name: hostname, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 1}
	rr.AAAA = net.ParseIP("")

	// craft reply
	rep := new(dns.Msg)
	rep.SetReply(req)
	rep.SetRcode(req, dns.RcodeSuccess)
	//rep.SetRcode(req, dns.RcodeNameError)
	rep.Answer = append(rep.Answer, rr)

	// send it
	w.WriteMsg(rep)
}
