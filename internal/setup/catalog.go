// Package setup implements 'jevtri init': it finds the known logs that
// exist on this host, lets the user pick them by number and writes the
// configuration file (spec 12.4, O-05).
package setup

// Family is the distribution family, which decides where logs live.
type Family int

const (
	FamilyUnknown Family = iota
	FamilyRHEL
	FamilyDebian
)

// known is one entry of the list in spec 12.4. Paths may be globs; a glob
// whose file names carry a date or weekday is written as the pattern itself
// (listed in Dated), any other glob is expanded into one candidate per file.
type known struct {
	Name   string
	RHEL   []string
	Debian []string
	// Formats are the catalog formats to try, most likely first. The one
	// written to the configuration must read the file (checked locally).
	RHELFormats   []string
	DebianFormats []string
	Dated         []string
}

// knownLogs is spec 12.4.1 to 12.4.4, confirmed by the user on 2026-09-28.
var knownLogs = []known{
	// OS
	{Name: "system", RHEL: []string{"/var/log/messages"}, Debian: []string{"/var/log/syslog"}, RHELFormats: []string{"syslog", "rfc3339"}, DebianFormats: []string{"rfc3339", "syslog"}},
	{Name: "auth", RHEL: []string{"/var/log/secure"}, Debian: []string{"/var/log/auth.log"}, RHELFormats: []string{"syslog", "rfc3339"}, DebianFormats: []string{"rfc3339", "syslog"}},
	{Name: "kernel", Debian: []string{"/var/log/kern.log"}, DebianFormats: []string{"rfc3339", "syslog"}},
	{Name: "cron", RHEL: []string{"/var/log/cron"}, Debian: []string{"/var/log/cron.log"}, RHELFormats: []string{"syslog", "rfc3339"}, DebianFormats: []string{"rfc3339", "syslog"}},
	{Name: "mail", RHEL: []string{"/var/log/maillog"}, Debian: []string{"/var/log/mail.log"}, RHELFormats: []string{"syslog", "rfc3339"}, DebianFormats: []string{"rfc3339", "syslog"}},
	{Name: "audit", RHEL: []string{"/var/log/audit/audit.log"}, Debian: []string{"/var/log/audit/audit.log"}, RHELFormats: []string{"epoch"}, DebianFormats: []string{"epoch"}},
	{Name: "package", RHEL: []string{"/var/log/dnf.log"}, Debian: []string{"/var/log/dpkg.log"}, RHELFormats: []string{"rfc3339"}, DebianFormats: []string{"iso-space"}},
	{Name: "apt", Debian: []string{"/var/log/apt/history.log"}, DebianFormats: []string{"iso-space"}},
	// Web and application servers
	{Name: "nginx-error", RHEL: []string{"/var/log/nginx/error.log"}, Debian: []string{"/var/log/nginx/error.log"}, RHELFormats: []string{"slash-ymd"}, DebianFormats: []string{"slash-ymd"}},
	{Name: "nginx-access", RHEL: []string{"/var/log/nginx/access.log"}, Debian: []string{"/var/log/nginx/access.log"}, RHELFormats: []string{"apache-access"}, DebianFormats: []string{"apache-access"}},
	{Name: "apache-error", RHEL: []string{"/var/log/httpd/error_log"}, Debian: []string{"/var/log/apache2/error.log"}, RHELFormats: []string{"apache-error"}, DebianFormats: []string{"apache-error"}},
	{Name: "apache-access", RHEL: []string{"/var/log/httpd/access_log"}, Debian: []string{"/var/log/apache2/access.log"}, RHELFormats: []string{"apache-access"}, DebianFormats: []string{"apache-access"}},
	{Name: "php-fpm", RHEL: []string{"/var/log/php-fpm/error.log"}, Debian: []string{"/var/log/php*-fpm.log"}, RHELFormats: []string{"dmy-month"}, DebianFormats: []string{"dmy-month"}},
	{Name: "tomcat", RHEL: []string{"/var/log/tomcat/catalina.*.log"}, Debian: []string{"/var/log/tomcat*/catalina.*.log"}, RHELFormats: []string{"dmy-month"}, DebianFormats: []string{"dmy-month"},
		Dated: []string{"/var/log/tomcat/catalina.*.log", "/var/log/tomcat*/catalina.*.log"}},
	{Name: "haproxy", RHEL: []string{"/var/log/haproxy.log"}, Debian: []string{"/var/log/haproxy.log"}, RHELFormats: []string{"syslog", "rfc3339", "apache-access"}, DebianFormats: []string{"rfc3339", "syslog", "apache-access"}},
	// Databases and caches
	{Name: "mysql", RHEL: []string{"/var/log/mysqld.log", "/var/log/mysql/mysqld.log"}, Debian: []string{"/var/log/mysql/error.log"}, RHELFormats: []string{"rfc3339"}, DebianFormats: []string{"rfc3339", "iso-space"}},
	{Name: "mariadb", RHEL: []string{"/var/log/mariadb/mariadb.log"}, Debian: []string{"/var/log/mysql/error.log"}, RHELFormats: []string{"iso-space"}, DebianFormats: []string{"iso-space", "rfc3339"}},
	{Name: "postgresql", RHEL: []string{"/var/lib/pgsql/*/data/log/*.log", "/var/lib/pgsql/data/log/*.log"}, Debian: []string{"/var/log/postgresql/postgresql-*.log"}, RHELFormats: []string{"iso-space"}, DebianFormats: []string{"iso-space"},
		// RHEL's default log_filename is postgresql-%a.log (one per weekday).
		Dated: []string{"/var/lib/pgsql/*/data/log/*.log", "/var/lib/pgsql/data/log/*.log"}},
	{Name: "redis", RHEL: []string{"/var/log/redis/redis.log"}, Debian: []string{"/var/log/redis/redis-server.log"}, RHELFormats: []string{"dmy-month"}, DebianFormats: []string{"dmy-month"}},
	{Name: "mongodb", RHEL: []string{"/var/log/mongodb/mongod.log"}, Debian: []string{"/var/log/mongodb/mongod.log"}, RHELFormats: []string{"rfc3339"}, DebianFormats: []string{"rfc3339"}},
	// Security and others
	{Name: "fail2ban", RHEL: []string{"/var/log/fail2ban.log"}, Debian: []string{"/var/log/fail2ban.log"}, RHELFormats: []string{"iso-comma"}, DebianFormats: []string{"iso-comma"}},
	{Name: "firewalld", RHEL: []string{"/var/log/firewalld"}, Debian: []string{"/var/log/firewalld"}, RHELFormats: []string{"iso-space"}, DebianFormats: []string{"iso-space"}},
	{Name: "sssd", RHEL: []string{"/var/log/sssd/*.log"}, Debian: []string{"/var/log/sssd/*.log"}, RHELFormats: []string{"iso-space", "apache-error"}, DebianFormats: []string{"iso-space", "apache-error"}},
	{Name: "cloud-init", RHEL: []string{"/var/log/cloud-init.log"}, Debian: []string{"/var/log/cloud-init.log"}, RHELFormats: []string{"iso-comma"}, DebianFormats: []string{"iso-comma"}},
}
