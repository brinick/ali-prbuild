package prbuild

import (
	"context"
	"fmt"
	"github.com/brinick/shell"
	"os"
	"runtime"
	"strings"
	"time"
)

// ------------------------------------------------------------------

type analyticsData struct {
	evt  string
	opts map[string]interface{}
}

func (a *analyticsData) screenview(value string) analyticsData {
	return analyticsData{
		evt: "screenview",
		opts: map[string]interface{}{
			"cd": value,
		},
	}
}

func (a *analyticsData) exception(desc string, fatal bool) analyticsData {
	return analyticsData{
		evt: "exception",
		opts: map[string]interface{}{
			"exd": desc,
			"exf": map[bool]int{true: 1, false: 0},
		},
	}
}

// ------------------------------------------------------------------

func newAnalyticsSvc(arch, id, appName, appVers string, maxCurlTime int) *analyticsSvc {
	a := analyticsSvc{
		id:          id,
		appName:     appName,
		appVers:     appVers,
		userAgent:   createUserAgent(arch),
		maxCurlTime: maxCurlTime,
		senderService: senderService{
			started: time.Now().Unix(),
		},
	}

	a.opts = a.baseOpts()
	return &a

}

type analyticsSvc struct {
	senderService
	id          string
	name        string
	appName     string
	appVers     string
	userAgent   string
	opts        map[string]interface{}
	maxCurlTime int
}

func (a *analyticsSvc) Name() string {
	return "analytics"
}

func (a *analyticsSvc) send(data interface{}) error {
	return a.sendWithContext(context.TODO(), data)
}

func (a *analyticsSvc) sendWithContext(ctx context.Context, data interface{}) error {
	andata, ok := data.(*analyticsData)
	if !ok {
		// wrong type passed in
		return fmt.Errorf(
			"Wrong data type passed to Analytics service send() - got %T, need *analyticsData",
			data,
		)

	}

	var err error

	defer func() {
		a.updateSendCounts(err)
	}()

	// merge in the data options for this event
	a.opts["t"] = andata.evt
	for k, v := range andata.opts {
		a.opts[k] = v
	}

	cmd := "curl " + a.curlArgs()
	err = shell.RunWithContext(ctx, cmd).Error
	return err
}

func (a *analyticsSvc) curlArgs() string {
	// update opts hash with metadata hash
	args := []string{
		fmt.Sprintf("--max-time %d", a.maxCurlTime),
		fmt.Sprintf("--user-agent %s", a.userAgent),
	}

	args = append(args, a.curlDataArgs()...)

	args = append(
		args,
		[]string{
			"--silent",
			"--output",
			"/dev/null",
			"https://www.google-analytics.com/collect",
		}...,
	)

	return strings.Join(args, " ")
}

func (a *analyticsSvc) curlDataArgs() []string {
	args := []string{}
	for k, v := range a.opts {
		arg := a.curlDataArg(k, v)
		if len(arg) > 0 {
			args = append(args, []string{"-d", arg}...)
		}
	}

	return args
}

func (a *analyticsSvc) curlDataArg(k string, v interface{}) string {
	arg := ""

	switch v.(type) {
	case int:
		v := v.(int)
		if v > 0 {
			arg = fmt.Sprintf("%s=%d", k, v)
		}

	case float64:
		v := v.(float64)
		if v > 0.0 {
			arg = fmt.Sprintf("%s=%f", k, v)
		}

	case string:
		v := v.(string)
		if len(v) > 0 {
			arg = fmt.Sprintf("%s=%s", k, v)
			if len(strings.Split(v, " ")) > 1 {
				arg = fmt.Sprintf("%s=\"%s\"", k, v)
			}
		}
	}

	return arg
}

func (a *analyticsSvc) baseOpts() map[string]interface{} {
	return map[string]interface{}{
		"v":   "1",
		"aip": "1",
		"tid": cfg.service.analytics.id,
		"cid": cfg.service.analytics.userID,
		"an":  cfg.service.analytics.appName,
		"av":  cfg.service.analytics.appVers,
	}
}

// ------------------------------------------------------------------
// Helper functions
// ------------------------------------------------------------------

func alibotVersion() string {
	return os.Getenv("ALIBOT_VERSION")
}

func golangVersion() string {
	return runtime.Version()
}

func osInfo(arch string) string {
	osType := "Linux"
	if strings.HasPrefix(arch, "osx") {
		osType = "Macintosh"
	}

	archToks := strings.SplitN(arch, "_", 2)
	osVersion, osProcessor := archToks[0], archToks[1]
	return fmt.Sprintf("%s; %s %s", osType, osProcessor, osVersion)
}

func createUserAgent(arch string) string {
	ua := []string{
		fmt.Sprintf("report-analytics/%s", alibotVersion()),
		osInfo(arch),
		golangVersion(),
	}

	return fmt.Sprintf("\"%s\"", strings.Join(ua, " "))
}
