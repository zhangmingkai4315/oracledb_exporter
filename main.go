package main

import (
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-oci8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
)

var (
	// Version will be set at build time.
	Version         = "0.0.0.dev"
	listenAddress   = flag.String("web.listen-address", ":9161", "Address to listen on for web interface and telemetry.")
	metricPath      = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	dataGuardBackup = flag.Bool("dataguard.backup", false, "Dataguard backup database")
	landingPage     = []byte("<html><head><title>Oracle DB Exporter " + Version + "</title></head><body><h1>Oracle DB Exporter " + Version + "</h1><p><a href='" + *metricPath + "'>Metrics</a></p></body></html>")
)

// Metric name parts.
const (
	namespace = "oracledb"
	exporter  = "exporter"
)

// Exporter collects Oracle DB metrics. It implements prometheus.Collector.
type Exporter struct {
	dsn             string
	duration, error prometheus.Gauge
	totalScrapes    prometheus.Counter
	scrapeErrors    *prometheus.CounterVec
	up              prometheus.Gauge
}

// NewExporter returns a new Oracle DB exporter for the provided DSN.
func NewExporter(dsn string) *Exporter {
	return &Exporter{
		dsn: dsn,
		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "last_scrape_duration_seconds",
			Help:      "Duration of the last scrape of metrics from Oracle DB.",
		}),
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "scrapes_total",
			Help:      "Total number of times Oracle DB was scraped for metrics.",
		}),
		scrapeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "scrape_errors_total",
			Help:      "Total number of times an error occured scraping a Oracle database.",
		}, []string{"collector"}),
		error: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "last_scrape_error",
			Help:      "Whether the last scrape of metrics from Oracle DB resulted in an error (1 for error, 0 for success).",
		}),
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "Whether the Oracle database server is up.",
		}),
	}
}

// Describe describes all the metrics exported by the MS SQL exporter.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	metricCh := make(chan prometheus.Metric)
	doneCh := make(chan struct{})

	go func() {
		for m := range metricCh {
			ch <- m.Desc()
		}
		close(doneCh)
	}()

	e.Collect(metricCh)
	close(metricCh)
	<-doneCh

}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.scrape(ch)
	ch <- e.duration
	ch <- e.totalScrapes
	ch <- e.error
	e.scrapeErrors.Collect(ch)
	ch <- e.up
}

func (e *Exporter) scrape(ch chan<- prometheus.Metric) {
	e.totalScrapes.Inc()
	var err error
	defer func(begun time.Time) {
		e.duration.Set(time.Since(begun).Seconds())
		if err == nil {
			e.error.Set(0)
		} else {
			e.error.Set(1)
		}
	}(time.Now())

	db, err := sql.Open("oci8", e.dsn)
	if err != nil {
		log.Errorln("Error opening connection to database:", err)
		return
	}
	defer db.Close()

	isUpRows, err := db.Query("SELECT 1 FROM DUAL")
	if err != nil {
		log.Errorln("Error pinging oracle:", err)
		e.up.Set(0)
		return
	}
	isUpRows.Close()
	e.up.Set(1)

	if err = ScrapeVersion(db, ch); err != nil {
		log.Errorln("Error scraping for version:", err)
		e.scrapeErrors.WithLabelValues("version").Inc()
	}

	if err = ScrapeDBARegistry(db, ch); err != nil {
		log.Errorln("Error scraping for DBARegistry:", err)
		e.scrapeErrors.WithLabelValues("dbaRegistry").Inc()
	}

	if err = ScrapeHighWaterMarkStatics(db, ch); err != nil {
		log.Errorln("Error scraping for highWaterMarkStatics:", err)
		e.scrapeErrors.WithLabelValues("highWaterMarkStatics").Inc()
	}

	if err = ScrapeInstanceOverview(db, ch); err != nil {
		log.Errorln("Error scraping for InstanceOverview:", err)
		e.scrapeErrors.WithLabelValues("instanceOverview").Inc()
	}

	if err = ScrapeDatabaseOverview(db, ch); err != nil {
		log.Errorln("Error scraping for DatabaseOverview:", err)
		e.scrapeErrors.WithLabelValues("databaseOverview").Inc()
	}

	if err = ScrapeActivity(db, ch); err != nil {
		log.Errorln("Error scraping for activity:", err)
		e.scrapeErrors.WithLabelValues("activity").Inc()
	}
	if err = ScrapeSPFile(db, ch); err != nil {
		log.Errorln("Error scraping for check SPFile:", err)
		e.scrapeErrors.WithLabelValues("spfile").Inc()
	}

	if err = ScrapeControlFiles(db, ch); err != nil {
		log.Errorln("Error scraping for ControlFiles:", err)
		e.scrapeErrors.WithLabelValues("controlfiles").Inc()
	}
	if err = ScrapeRedoLogSwitch(db, ch); err != nil {
		log.Errorln("Error scraping for RedoLogSwitch:", err)
		e.scrapeErrors.WithLabelValues("redologswitch").Inc()
	}
	if err = ScrapeOnlineRedoLog(db, ch); err != nil {
		log.Errorln("Error scraping for OnlineRedoLog:", err)
		e.scrapeErrors.WithLabelValues("redologbytes").Inc()
	}

	if err = ScrapeTablespace(db, ch); err != nil {
		log.Errorln("Error scraping for tablespace:", err)
		e.scrapeErrors.WithLabelValues("tablespace").Inc()
	}
	if err = ScrapeInvalidObjectTotal(db, ch); err != nil {
		log.Errorln("Error scraping for invalid object:", err)
		e.scrapeErrors.WithLabelValues("invalidtotal").Inc()
	}
	if err = ScrapeDataGuardStatus(db, ch); err != nil {
		log.Errorln("Error scraping for dataguard:", err)
		e.scrapeErrors.WithLabelValues("dataguard").Inc()
	}

	if err = ScrapeSessions(db, ch); err != nil {
		log.Errorln("Error scraping for sessions:", err)
		e.scrapeErrors.WithLabelValues("sessions").Inc()
	}

}

// ScrapeVersion collect core, oracle ,NLSRTL, PL/SQL version info
func ScrapeVersion(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("SELECT * FROM v$version")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var version string
		if err = rows.Scan(&version); err != nil {
			return err
		}
		version = strings.Replace(version, "\t", " ", -1)
		name := strings.Split(version, " ")[0]
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "version", "info"),
				"Metrics for version of oracledb", []string{"name", "version"}, nil),
			prometheus.GaugeValue,
			1.0,
			name,
			version,
		)
	}
	return nil
}

// ScrapeDBARegistry collect core, oracle ,NLSRTL, PL/SQL version info
func ScrapeDBARegistry(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("select comp_id,comp_name,version,status,modified,control,schema,procedure from dba_registry ORDER BY comp_name")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			compID    sql.NullString
			compName  sql.NullString
			version   sql.NullString
			status    sql.NullString
			modified  sql.NullString
			control   sql.NullString
			schema    sql.NullString
			procedure sql.NullString
		)
		if err = rows.Scan(&compID, &compName, &version, &status, &modified, &control, &schema, &procedure); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "dba", "registry"),
				"Metrics for dba registry of oracledb", []string{"comp_id", "comp_name", "version", "status", "modified", "control", "schema", "procedure"}, nil),
			prometheus.GaugeValue,
			1.0,
			compID.String,
			compName.String,
			version.String,
			status.String,
			modified.String,
			control.String,
			schema.String,
			procedure.String,
		)
	}
	return nil
}

// ScrapeHighWaterMarkStatics collect high water mark info
func ScrapeHighWaterMarkStatics(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("SELECT  name \"statistic_name\",highwater highwater , last_value last_value , description description FROM dba_high_water_mark_statistics ORDER BY name")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name        string
			highwater   sql.NullFloat64
			lastValue   sql.NullFloat64
			description sql.NullString
		)
		if err = rows.Scan(&name, &highwater, &lastValue, &description); err != nil {

			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "highwatermark", "mark"),
				"High water mark  infomation", []string{"name"}, nil),
			prometheus.GaugeValue,
			highwater.Float64,
			name,
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "highwatermark", "last_value"),
				"High water mark statistics infomation with last value", []string{"name"}, nil),
			prometheus.GaugeValue,
			lastValue.Float64,
			name,
		)
	}
	return nil
}

// ScrapeInstanceOverview collect instance overview information
func ScrapeInstanceOverview(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("select instance_name,instance_number,thread#,host_name,version,startup_time,parallel,status,logins,archiver  from gv$instance")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name        string
			number      float64
			thread      int
			host        sql.NullString
			version     sql.NullString
			startUpTime sql.NullString
			parallel    sql.NullString
			status      sql.NullString
			logins      sql.NullString
			archiver    sql.NullString
		)
		if err = rows.Scan(&name, &number, &thread,
			&host, &version, &startUpTime, &parallel, &status, &logins, &archiver); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "instance", "info"),
				"instance overview and number", []string{"name", "thread", "host", "version", "startUpTime", "parallel", "status", "logins", "archiver"}, nil),
			prometheus.GaugeValue,
			number,
			name,
			fmt.Sprint(thread),
			host.String,
			version.String,
			startUpTime.String,
			parallel.String,
			status.String,
			logins.String,
			archiver.String,
		)
	}
	return nil
}

// ScrapeDatabaseOverview collect instance overview information
func ScrapeDatabaseOverview(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("select name,db_unique_name,dbid,open_mode,created,platform_name,database_role,controlfile_type,current_scn,log_mode,FORCE_LOGGING,flashback_on from v$database")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name            string
			uniqueName      sql.NullString
			dbid            float64
			openMode        sql.NullString
			created         sql.NullString
			platformName    sql.NullString
			role            sql.NullString
			controlfileType sql.NullString
			currentSCN      sql.NullString
			logMode         sql.NullString
			forceLogging    sql.NullString
			flashback       sql.NullString
		)
		if err = rows.Scan(&name, &uniqueName, &dbid,
			&openMode, &created, &platformName, &role,
			&controlfileType, &currentSCN, &logMode, &forceLogging, &flashback); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "database", "info"),
				"database overview and number", []string{"name", "db_unique_name", "dbid",
					"open_mode", "created", "platform_name", "database_role",
					"controlfile_type", "current_scn", "log_mode", "FORCE_LOGGING", "flashback_on"}, nil),
			prometheus.GaugeValue,
			1,
			name,
			uniqueName.String,
			fmt.Sprint(dbid),
			openMode.String,
			created.String,
			platformName.String,
			role.String,
			controlfileType.String,
			currentSCN.String,
			logMode.String,
			forceLogging.String,
			flashback.String,
		)
	}
	return nil
}

// ScrapeSPFile collect database if it using spfile
func ScrapeSPFile(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("select (1-SIGN(1-SIGN(count(*) - 0))) FROM v$spparameter WHERE value IS NOT null")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var usingSPF float64
		if err = rows.Scan(&usingSPF); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "using", "spfile"),
				"Metrics for get if the database using spf", []string{}, nil),
			prometheus.GaugeValue,
			usingSPF,
		)
	}
	return nil
}

// ScrapeControlFiles collect instance control files information
func ScrapeControlFiles(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("select name,DECODE( c.status,NULL,'VALID','' || c.status || '') status,''|| TO_CHAR(block_size * file_size_blks, '999999999999') || '' file_size  from v$controlfile c ORDER BY c.name")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name     string
			status   sql.NullString
			fileSize sql.NullString
		)
		if err = rows.Scan(&name, &status, &fileSize); err != nil {
			return err
		}
		fileSizeFloat64, err := strconv.ParseFloat(strings.Replace(fileSize.String, " ", "", -1), 64)
		if err != nil {
			fileSizeFloat64 = -1
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "controlfile", "bytes"),
				"control file and status infomation", []string{"name", "status"}, nil),
			prometheus.GaugeValue,
			fileSizeFloat64,
			name,
			status.String,
		)
	}
	return nil
}

// ScrapeOnlineRedoLog collect online redo file information
func ScrapeOnlineRedoLog(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("SELECT '' || i.instance_name || '' instance_name_print, '' || i.thread# || '' thread_number_print,f.group# groupno,'' || f.member || '' member,f.type redo_file_type,DECODE( l.status,'CURRENT','' || l.status || '','' || l.status || '') log_status,l.bytes bytes, '' || l.archived || '' archived FROM gv$logfile f ,gv$log l, gv$instance i WHERE f.group# = l.group# AND l.thread# = i.thread# AND i.inst_id = f.inst_id AND f.inst_id = l.inst_id ORDER BY i.instance_name , f.group# , f.member")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name     string
			thread   sql.NullString
			group    sql.NullString
			member   sql.NullString
			fileType sql.NullString
			status   sql.NullString
			bytes    float64
			archived sql.NullString
		)
		if err = rows.Scan(
			&name, &thread, &group, &member, &fileType,
			&status, &bytes, &archived); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "redolog", "bytes"),
				"online redo file infomation", []string{"name", "thread", "group", "member", "fileType", "status", "archived"}, nil),
			prometheus.GaugeValue,
			bytes,
			name,
			thread.String,
			group.String,
			member.String,
			fileType.String,
			status.String,
			archived.String,
		)
	}
	return nil
}

// ScrapeRedoLogSwitch collect redo switch informations
func ScrapeRedoLogSwitch(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("SELECT ''|| SUBSTR(TO_CHAR(first_time, 'yyyy-MM-DD HH:MI:SS'),1,7) ||'' \"DATE\",count(*) TOTAL from v$log_history group by SUBSTR(TO_CHAR(first_time, 'yyyy-MM-DD HH:MI:SS'),1,7) ORDER BY SUBSTR(TO_CHAR(first_time, 'yyyy-MM-DD HH:MI:SS'),1,7)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			date  string
			total float64
		)
		if err = rows.Scan(&date, &total); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "redolog", "switchs"),
				"Redo Log Switches", []string{"date"}, nil),
			prometheus.GaugeValue,
			total,
			date,
		)
	}
	return nil
}

// ScrapeSessions collects session metrics from the v$session view.
func ScrapeSessions(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	// Retrieve status and type for all sessions.
	rows, err = db.Query("SELECT status, type, COUNT(*) FROM v$session GROUP BY status, type")
	if err != nil {
		return err
	}

	defer rows.Close()
	activeCount := 0.
	inactiveCount := 0.
	for rows.Next() {
		var (
			status      string
			sessionType string
			count       float64
		)
		if err := rows.Scan(&status, &sessionType, &count); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sessions", "activity"),
				"Gauge metric with count of sessions by status and type", []string{"status", "type"}, nil),
			prometheus.GaugeValue,
			count,
			status,
			sessionType,
		)

		// These metrics are deprecated though so as to not break existing monitoring straight away, are included for the next few releases.
		if status == "ACTIVE" {
			activeCount += count
		}

		if status == "INACTIVE" {
			inactiveCount += count
		}
	}

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(prometheus.BuildFQName(namespace, "sessions", "active"),
			"Gauge metric with count of sessions marked ACTIVE. DEPRECATED: use sum(oracledb_sessions_activity{status='ACTIVE}) instead.", []string{}, nil),
		prometheus.GaugeValue,
		activeCount,
	)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(prometheus.BuildFQName(namespace, "sessions", "inactive"),
			"Gauge metric with count of sessions marked INACTIVE. DEPRECATED: use sum(oracledb_sessions_activity{status='INACTIVE'}) instead.", []string{}, nil),
		prometheus.GaugeValue,
		inactiveCount,
	)
	return nil
}

// ScrapeActivity collects activity metrics from the v$sysstat view.
func ScrapeActivity(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("SELECT name, value FROM v$sysstat WHERE name IN ('parse count (total)', 'execute count', 'user commits', 'user rollbacks')")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var value float64
		if err := rows.Scan(&name, &value); err != nil {
			return err
		}
		name = cleanName(name)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "activity", name),
				"Generic counter metric from v$sysstat view in Oracle.", []string{}, nil),
			prometheus.CounterValue,
			value,
		)
	}
	return nil
}

// ScrapeTablespace collects tablespace size.
func ScrapeTablespace(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query(`
SELECT UPPER(F.TABLESPACE_NAME) "tablespace_name",
            D.TOT_GROOTTE_BYTES "tablespace_size",
            D.TOT_GROOTTE_BYTES-F.TOTAL_BYTES "tablespace_used",
			F.TOTAL_BYTES "tablespace_free(M)"
     FROM (SELECT TABLESPACE_NAME,
                  ROUND(SUM(BYTES)) TOTAL_BYTES,
                  ROUND(MAX(BYTES),2) MAX_BYTES
           FROM SYS.DBA_FREE_SPACE
           GROUP BY TABLESPACE_NAME) F,
          (SELECT DD.TABLESPACE_NAME,
                  ROUND(SUM(BYTES)) TOT_GROOTTE_BYTES
           FROM SYS.DBA_DATA_FILES DD
           GROUP BY DD.TABLESPACE_NAME) D
     WHERE D.TABLESPACE_NAME=F.TABLESPACE_NAME
     ORDER BY 2 DESC
`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tablespaceTotalBytesDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "tablespace", "total_bytes"),
		"Generic counter metric of tablespaces bytes in Oracle.",
		[]string{"tablespace"}, nil,
	)
	tablespaceUsedBytesDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "tablespace", "used_bytes"),
		"Generic counter metric of tablespaces max bytes in Oracle.",
		[]string{"tablespace"}, nil,
	)
	tablespaceFreeBytesDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "tablespace", "free_bytes"),
		"Generic counter metric of tablespaces free bytes in Oracle.",
		[]string{"tablespace"}, nil,
	)

	for rows.Next() {
		var (
			name  string
			total float64
			used  float64
			free  float64
		)
		if err := rows.Scan(&name, &total, &used, &free); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(tablespaceTotalBytesDesc, prometheus.GaugeValue, float64(total), name)
		ch <- prometheus.MustNewConstMetric(tablespaceUsedBytesDesc, prometheus.GaugeValue, float64(used), name)
		ch <- prometheus.MustNewConstMetric(tablespaceFreeBytesDesc, prometheus.GaugeValue, float64(free), name)
	}
	return nil
}

// ScrapeInvalidObjectTotal collects invalid object total.
func ScrapeInvalidObjectTotal(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("select count(*) from dba_objects where status = 'INVALID'")
	if err != nil {
		return err
	}
	defer rows.Close()
	invalidTotalDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "invalid", "total"),
		"Generic invalid total count.",
		[]string{"name"}, nil,
	)
	var count float64
	for rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(invalidTotalDesc, prometheus.GaugeValue, float64(count), "objects")
	}
	rows, err = db.Query("select count(*) from dba_indexes where status <> upper('VALID') AND OWNER NOT LIKE 'SYS%'")
	if err != nil {
		return err
	}
	for rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(invalidTotalDesc, prometheus.GaugeValue, float64(count), "indexes")
	}
	return nil
}

// ScrapeDataGuardStatus collects dataguard information total.
func ScrapeDataGuardStatus(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	dataGuardSequenceDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "dataguard", "sequence"),
		"Generic Sequence from dataguard.",
		[]string{"group", "thread", "status"}, nil,
	)
	dataGuardBytesDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "dataguard", "bytes"),
		"Generic bytes information from dataguard.",
		[]string{"group", "thread", "status"}, nil,
	)
	dataGuardMaxSequenceDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "dataguard", "max_sequence"),
		"Generic max sequence information from dataguard.",
		[]string{"thread"}, nil,
	)
	rows, err = db.Query("select a.GROUP#,THREAD#,SEQUENCE#,BYTES \"bytes\",a.status from v$log a,v$logfile b where a.GROUP#=b.GROUP# and a.status='CURRENT' order by 1,2")
	if err != nil {
		return err
	}
	defer rows.Close()
	var (
		group    sql.NullString
		thread   sql.NullString
		sequence float64
		bytes    float64
		status   sql.NullString
	)
	for rows.Next() {
		if err := rows.Scan(&group, &thread, &sequence, &bytes, &status); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			dataGuardSequenceDesc,
			prometheus.GaugeValue,
			float64(sequence),
			group.String,
			thread.String,
			status.String)
		ch <- prometheus.MustNewConstMetric(
			dataGuardBytesDesc,
			prometheus.GaugeValue,
			float64(bytes),
			group.String,
			thread.String,
			status.String)
	}
	if *dataGuardBackup == false {
		return nil
	}
	rows, err = db.Query("SELECT THREAD#, MAX(SEQUENCE#) \"SEQUENCE#\" FROM V$ARCHIVED_LOG val, V$DATABASE vdb WHERE APPLIED = 'YES' AND val.RESETLOGS_CHANGE#=vdb.RESETLOGS_CHANGE#  GROUP BY THREAD#")
	if err != nil {
		return err
	}
	var (
		threadID    string
		maxSequence float64
	)
	for rows.Next() {
		if err := rows.Scan(&threadID, &maxSequence); err != nil {
			// Backup database only
			return nil
		}
		ch <- prometheus.MustNewConstMetric(
			dataGuardMaxSequenceDesc,
			prometheus.GaugeValue,
			float64(maxSequence),
			threadID,
		)
	}
	return nil
}

// Oracle gives us some ugly names back. This function cleans things up for Prometheus.
func cleanName(s string) string {
	s = strings.Replace(s, " ", "_", -1) // Remove spaces
	s = strings.Replace(s, "(", "", -1)  // Remove open parenthesis
	s = strings.Replace(s, ")", "", -1)  // Remove close parenthesis
	s = strings.Replace(s, "/", "", -1)  // Remove forward slashes
	s = strings.ToLower(s)
	return s
}

func main() {
	flag.Parse()
	log.Infoln("Starting oracledb_exporter " + Version)
	dsn := os.Getenv("DATA_SOURCE_NAME")
	exporter := NewExporter(dsn)
	prometheus.MustRegister(exporter)
	http.Handle(*metricPath, prometheus.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(landingPage)
	})
	log.Infoln("Listening on", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
