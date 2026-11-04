variable "name" {
  description = "Prefix for every resource name (e.g. docmatch-prod)."
  type        = string

  validation {
    condition     = can(regex("^docmatch-[a-z0-9-]+$", var.name))
    error_message = "name must start with docmatch-."
  }
}

variable "env" {
  description = "Environment name."
  type        = string
}

variable "account_id" {
  description = "AWS account id (KMS key policy, log delivery conditions)."
  type        = string
}

variable "certificate_key_algorithm" {
  description = "Key algorithm of the ACM certificate. ECDSA P-256 is accepted by CloudFront and every modern client."
  type        = string
  default     = "EC_prime256v1"

  validation {
    condition     = contains(["RSA_2048", "EC_prime256v1", "EC_secp384r1"], var.certificate_key_algorithm)
    error_message = "certificate_key_algorithm must be RSA_2048, EC_prime256v1 or EC_secp384r1."
  }
}

variable "domain" {
  description = "Apex domain served by this environment (a Route 53 public zone is created for it)."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9-]+\\.)+[a-z]{2,}$", var.domain))
    error_message = "domain must be a lowercase DNS name without trailing dot."
  }
}

variable "origin_hostname" {
  description = "Hostname of the application origin (the Elastic IP record created by the compute module)."
  type        = string
}

variable "origin_secret_header_name" {
  description = "Request header the CloudFront function adds from the key-value store; Caddy rejects requests without it."
  type        = string
  default     = "x-docmatch-origin-secret"
}

variable "api_path_patterns" {
  description = "Path patterns routed to the application origin instead of the static site."
  type        = list(string)
  default     = ["/api/*", "/auth/*", "/webhooks/*"]
}

variable "site_bucket_name" {
  description = "Name of the static site bucket."
  type        = string
}

variable "site_bucket_arn" {
  description = "ARN of the static site bucket."
  type        = string
}

variable "site_bucket_regional_domain_name" {
  description = "Regional domain name of the static site bucket."
  type        = string
}

variable "logs_bucket_arn" {
  description = "ARN of the logs bucket for CloudFront standard logs (v2)."
  type        = string
}

variable "enable_access_logs" {
  description = "Deliver CloudFront standard logs (v2) to the logs bucket."
  type        = bool
  default     = true
}

variable "dnssec_enabled" {
  description = "Sign the zone with a KMS-backed key-signing key. The DS record must then be published at the registrar."
  type        = bool
  default     = true
}

variable "wait_for_certificate_validation" {
  description = "Wait for the ACM certificate to validate (requires the zone's NS to be delegated). Set to false on the very first apply, before the registrar points at Route 53."
  type        = bool
  default     = true
}

variable "price_class" {
  description = "CloudFront price class. South America is only included in PriceClass_All."
  type        = string
  default     = "PriceClass_All"
}

variable "apex_spf" {
  description = "SPF TXT published at the apex. Empty publishes none."
  type        = string
  default     = "v=spf1 -all"
}

variable "dmarc_rua_email" {
  description = "Mailbox for DMARC aggregate reports. Empty publishes a DMARC record without rua."
  type        = string
  default     = ""
}

variable "extra_records" {
  description = "Additional DNS records (DKIM, verifications, delegations) as {name, type, ttl, records}. name is relative to the zone; empty name is the apex."
  type = list(object({
    name    = string
    type    = string
    ttl     = number
    records = list(string)
  }))
  default = []
}

variable "content_security_policy" {
  description = "Content-Security-Policy sent for static responses. The application sets its own nonce-based policy for dynamic pages (override is false)."
  type        = string
  default     = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; media-src 'self' blob:; worker-src 'self' blob:; manifest-src 'self'; form-action 'self'; base-uri 'self'; frame-ancestors 'none'; upgrade-insecure-requests"
}

variable "permissions_policy" {
  description = "Permissions-Policy header value."
  type        = string
  default     = "accelerometer=(), autoplay=(), camera=(), display-capture=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), picture-in-picture=(), publickey-credentials-get=(self), screen-wake-lock=(), usb=(), xr-spatial-tracking=(), interest-cohort=()"
}

variable "hsts_max_age_seconds" {
  description = "Strict-Transport-Security max-age. Two years is the preload list minimum."
  type        = number
  default     = 63072000
}

variable "scanner_user_agent_regex" {
  description = "Case-insensitive regular expression of user agents answered with an empty 404 at the edge. Keep it to real scanners: API SDKs and curl must keep working."
  type        = string
  default     = "sqlmap|nikto|nmap|masscan|zgrab|zmap|nuclei|acunetix|nessus|openvas|w3af|dirbuster|gobuster|feroxbuster|ffuf|wpscan|whatweb|arachni|netsparker|jaeles|dalfox|xsser|havij|wfuzz|commix|joomscan|droopescan|cmsmap|shodan|censys|expanse|internetmeasurement|paloaltonetworks|zoominfobot|petalbot|bytespider|mj12bot|semrushbot|ahrefsbot|dotbot"
}

variable "probe_path_prefixes" {
  description = "Request paths (prefix match, lowercased) answered with an empty 404 at the edge."
  type        = list(string)
  default = [
    "/.env", "/.git", "/.svn", "/.hg", "/.aws", "/.ssh", "/.docker", "/.vscode", "/.idea", "/.ds_store",
    "/wp-admin", "/wp-login.php", "/wp-content", "/wp-includes", "/wp-json", "/xmlrpc.php", "/wordpress",
    "/cgi-bin", "/phpmyadmin", "/pma", "/myadmin", "/mysql", "/adminer", "/phpinfo", "/info.php",
    "/server-status", "/server-info", "/actuator", "/console", "/jmx-console", "/manager/html",
    "/vendor/phpunit", "/telescope", "/_profiler", "/debug", "/elmah.axd", "/trace.axd",
    "/owa/", "/ecp/", "/autodiscover", "/remote/login", "/boaform", "/hnap1", "/shell", "/cmd",
    "/solr", "/jenkins", "/geoserver", "/struts", "/hudson", "/webui", "/aws/credentials",
    "/config.json", "/config.php", "/configuration.php", "/backup", "/dump.sql", "/db.sql",
  ]
}

variable "probe_path_suffixes" {
  description = "Request path suffixes (lowercased) answered with an empty 404 at the edge."
  type        = list(string)
  default     = [".php", ".php7", ".phtml", ".asp", ".aspx", ".jsp", ".jspx", ".cgi", ".pl", ".sql", ".bak", ".old", ".orig", ".swp", ".ini", ".cfg", ".env"]
}

variable "tags" {
  description = "Extra tags merged into every resource."
  type        = map(string)
  default     = {}
}
