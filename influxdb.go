package prbuild

import (
	"context"
	"fmt"
	"github.com/brinick/shell"
	"strings"
	"time"
)

// ------------------------------------------------------------------

type influxDBData struct {
	state       string
	prBuildTime int64
	prNumber    int
	prSucceeded bool
}

// ------------------------------------------------------------------

func newInfluxDBSvc(
	host, ciHash, checkName, writeURL string,
	allowInsecureCurl bool,
	maxCurlTime int,
) *influxDB {

	return &influxDB{
		host:              host,
		ciHash:            ciHash,
		checkName:         checkName,
		url:               writeURL,
		allowInsecureCurl: allowInsecureCurl,
		maxCurlTime:       maxCurlTime,
		senderService: senderService{
			started: time.Now().Unix(),
		},
	}
}

// ------------------------------------------------------------------

type influxDB struct {
	senderService
	name              string
	host              string
	ciHash            string
	checkName         string
	url               string
	allowInsecureCurl bool
	maxCurlTime       int
}

func (i *influxDB) Name() string {
	return "influxDB"
}

func (i *influxDB) send(data interface{}) error {
	return i.sendWithContext(context.TODO(), data)
}

func (i *influxDB) sendWithContext(ctx context.Context, data interface{}) error {
	infdata, ok := data.(*influxDBData)
	if !ok {
		// wrong type passed in
		return fmt.Errorf(
			"Wrong data type passed to influxDB service send() - got %T, need *influxDBData",
			data,
		)
	}

	var (
		r   *shell.Result
		err error
	)

	defer func() {
		i.updateSendCounts(err)
	}()

	cmd := strings.Join(
		[]string{
			"curl",
			map[bool]string{true: "-k", false: ""}[i.allowInsecureCurl],
			fmt.Sprintf("--max-time %d", i.maxCurlTime),
			fmt.Sprintf("--XPOST \"%s\"", i.url),
			fmt.Sprintf("--data-binary \"%s\"", i.constructCurlArgs(infdata)),
		},
		" ",
	)

	if r = shell.RunWithContext(ctx, cmd); r.IsError() {
		Info("Unable to post data to InfluxDB", ErrField(r.Error))
	}

	return r.Error
}

func (i *influxDB) constructCurlArgs(data *influxDBData) string {
	host := fmt.Sprintf("host=\"%s\"", i.host)
	cn := fmt.Sprintf("checkname=%s", i.checkName)
	cnHost := fmt.Sprintf("%s %s", cn, host)
	state := fmt.Sprintf("state=\"%s\"", data.state)
	ciHash := fmt.Sprintf("cihash=\"%s\"", i.ciHash)

	now := time.Now()
	uptime := fmt.Sprintf("uptime=%d", (now.Unix() - i.started))

	prtime := ""
	if data.prBuildTime > 0 {
		prtime = fmt.Sprintf("prtime=%d", data.prBuildTime)
	}

	prID := ""
	if data.prNumber > 0 {
		prID = fmt.Sprintf("prid=\"%d\"", data.prNumber)
	}

	prOK := ""
	if data.prNumber > 0 {
		prOK = fmt.Sprintf("prok=\"%t\"", data.prSucceeded)
	}

	nowNanoSecs := fmt.Sprintf("%d", now.UnixNano())

	args := []string{"prcheck", cnHost, state, ciHash, uptime, prtime, prID, prOK}
	return strings.Join(args, ",") + " " + nowNanoSecs
}
