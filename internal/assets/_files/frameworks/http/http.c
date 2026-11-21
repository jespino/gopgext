/*
 * C side of gopgext/http: HTTP(S) transfers on libcurl's multi interface,
 * pulled incrementally by Go (response bodies are streamed, and waiting
 * notices query cancellation).
 *
 * Transfers are identified by ids, so a transfer closed by the C side can't
 * be used again from Go. Transfers still open at the end of the transaction
 * (commit or abort, e.g. a scan abandoned by an error) are closed then.
 *
 * None of these functions raise PostgreSQL errors: they are called from Go.
 */
#include "postgres.h"

#include <curl/curl.h>

#include "access/xact.h"
#include "miscadmin.h"

#include "gopgext.h"

typedef struct transfer
{
	struct transfer *next;
	int64		id;
	CURLM	   *multi;
	CURL	   *easy;
	struct curl_slist *headers;
	char	   *body;			/* request body (owned) */
	char	   *buf;			/* response body: received, unconsumed buf[off:len] */
	size_t		off,
				len,
				cap;
	char	   *hdr;			/* header lines of the current response */
	size_t		hdrlen,
				hdrcap;
	bool		hdr_done;		/* headers of the final response received */
	bool		location;		/* current header block has a Location */
	bool		done;
	bool		oom;
	CURLcode	result;
	char		error[CURL_ERROR_SIZE];
} transfer;

ErrorData  *gopgext_http_start(const char *method, const char *url, const char *headers,
							   const char *body, int64 body_len, int64 timeout_ms, char *errbuf,
							   int64 *id);
int64		gopgext_http_wait_headers(int64 id);
int64		gopgext_http_read(int64 id, const char **data, int64 *len);
void		gopgext_http_consume(int64 id, int64 n);
int64		gopgext_http_status(int64 id);
const char *gopgext_http_headers(int64 id);
const char *gopgext_http_error(int64 id);
void		gopgext_http_close(int64 id);

static transfer *transfers;
static int64 last_id;
static bool initialized;

static bool
grow(char **buf, size_t *cap, size_t need)
{
	char	   *p;
	size_t		n;

	if (need <= *cap)
		return true;
	n = Max(*cap * 2, need + 16384);
	p = realloc(*buf, n);
	if (!p)
		return false;
	*buf = p;
	*cap = n;
	return true;
}

static size_t
write_cb(char *data, size_t size, size_t n, void *arg)
{
	transfer   *t = arg;
	size_t		len = size * n;

	if (t->off > 0)
	{
		/* compact: move the unconsumed part to the start */
		memmove(t->buf, t->buf + t->off, t->len - t->off);
		t->len -= t->off;
		t->off = 0;
	}
	if (!grow(&t->buf, &t->cap, t->len + len))
	{
		t->oom = true;
		return 0;				/* aborts the transfer */
	}
	memcpy(t->buf + t->len, data, len);
	t->len += len;
	t->hdr_done = true;			/* body data: the headers are final */
	return len;
}

static size_t
header_cb(char *data, size_t size, size_t n, void *arg)
{
	transfer   *t = arg;
	size_t		len = size * n;

	if (len >= 5 && strncmp(data, "HTTP/", 5) == 0)
	{
		/* a new response (after a redirect or 100 Continue) */
		t->hdrlen = 0;
		t->location = false;
	}
	else if (len >= 9 && pg_strncasecmp(data, "location:", 9) == 0)
		t->location = true;
	else if (len <= 2 && (data[0] == '\r' || data[0] == '\n'))
	{
		long		code = 0;

		/* end of a header block: final unless 1xx or a followed redirect */
		curl_easy_getinfo(t->easy, CURLINFO_RESPONSE_CODE, &code);
		if (code >= 200 && !(code >= 300 && code < 400 && t->location))
			t->hdr_done = true;
	}
	if (!grow(&t->hdr, &t->hdrcap, t->hdrlen + len + 1))
	{
		t->oom = true;
		return 0;
	}
	memcpy(t->hdr + t->hdrlen, data, len);
	t->hdrlen += len;
	t->hdr[t->hdrlen] = '\0';
	return len;
}

static void
free_transfer(transfer *t)
{
	curl_multi_remove_handle(t->multi, t->easy);
	curl_easy_cleanup(t->easy);
	curl_multi_cleanup(t->multi);
	curl_slist_free_all(t->headers);
	free(t->body);
	free(t->buf);
	free(t->hdr);
	free(t);
}

static void
xact_cb(XactEvent event, void *arg)
{
	switch (event)
	{
		case XACT_EVENT_COMMIT:
		case XACT_EVENT_PARALLEL_COMMIT:
		case XACT_EVENT_ABORT:
		case XACT_EVENT_PARALLEL_ABORT:
		case XACT_EVENT_PREPARE:
			while (transfers)
			{
				transfer   *t = transfers;

				transfers = t->next;
				free_transfer(t);
			}
			break;
		default:
			break;
	}
}

static transfer *
lookup(int64 id)
{
	for (transfer *t = transfers; t; t = t->next)
		if (t->id == id)
			return t;
	return NULL;
}

/*
 * Starts a request. headers are "Name: value" lines separated by '\n'.
 * Returns the transfer id, or 0 with the reason in errbuf (256 bytes).
 */
static int64
http_start(const char *method, const char *url, const char *headers,
		   const char *body, int64 body_len, int64 timeout_ms, char *errbuf)
{
	transfer   *t;
	const char *p;

	if (!initialized)
	{
		if (curl_global_init(CURL_GLOBAL_DEFAULT) != CURLE_OK)
		{
			strlcpy(errbuf, "could not initialize libcurl", 256);
			return 0;
		}
		RegisterXactCallback(xact_cb, NULL);
		initialized = true;
	}
	t = calloc(1, sizeof(*t));
	if (!t)
	{
		strlcpy(errbuf, "out of memory", 256);
		return 0;
	}
	t->easy = curl_easy_init();
	t->multi = curl_multi_init();
	if (!t->easy || !t->multi)
	{
		strlcpy(errbuf, "could not create a libcurl handle", 256);
		if (t->easy)
			curl_easy_cleanup(t->easy);
		if (t->multi)
			curl_multi_cleanup(t->multi);
		free(t);
		return 0;
	}
	curl_easy_setopt(t->easy, CURLOPT_URL, url);
	curl_easy_setopt(t->easy, CURLOPT_PROTOCOLS_STR, "http,https");
	curl_easy_setopt(t->easy, CURLOPT_REDIR_PROTOCOLS_STR, "http,https");
	curl_easy_setopt(t->easy, CURLOPT_FOLLOWLOCATION, 1L);
	curl_easy_setopt(t->easy, CURLOPT_MAXREDIRS, 10L);
	curl_easy_setopt(t->easy, CURLOPT_WRITEFUNCTION, write_cb);
	curl_easy_setopt(t->easy, CURLOPT_WRITEDATA, t);
	curl_easy_setopt(t->easy, CURLOPT_HEADERFUNCTION, header_cb);
	curl_easy_setopt(t->easy, CURLOPT_HEADERDATA, t);
	curl_easy_setopt(t->easy, CURLOPT_ERRORBUFFER, t->error);
	curl_easy_setopt(t->easy, CURLOPT_USERAGENT, "gopgext-http/1.0");
	curl_easy_setopt(t->easy, CURLOPT_ACCEPT_ENCODING, "");
	curl_easy_setopt(t->easy, CURLOPT_NOSIGNAL, 1L);
	curl_easy_setopt(t->easy, CURLOPT_CONNECTTIMEOUT_MS,
					 (long) (timeout_ms > 0 ? Min(timeout_ms, (int64) 30000) : 30000));
	if (timeout_ms > 0)
		curl_easy_setopt(t->easy, CURLOPT_TIMEOUT_MS, (long) timeout_ms);
	if (method && strcmp(method, "GET") != 0)
	{
		if (strcmp(method, "HEAD") == 0)
			curl_easy_setopt(t->easy, CURLOPT_NOBODY, 1L);
		else
			curl_easy_setopt(t->easy, CURLOPT_CUSTOMREQUEST, method);
	}
	if (body)
	{
		t->body = malloc(Max(body_len, 1));
		if (!t->body)
		{
			strlcpy(errbuf, "out of memory", 256);
			free_transfer(t);
			return 0;
		}
		memcpy(t->body, body, body_len);
		curl_easy_setopt(t->easy, CURLOPT_POSTFIELDS, t->body);
		curl_easy_setopt(t->easy, CURLOPT_POSTFIELDSIZE_LARGE, (curl_off_t) body_len);
	}
	for (p = headers; p && *p;)
	{
		const char *end = strchr(p, '\n');
		size_t		n = end ? (size_t) (end - p) : strlen(p);
		char	   *line = strndup(p, n);

		if (line && n > 0)
			t->headers = curl_slist_append(t->headers, line);
		free(line);
		p += n + (end ? 1 : 0);
	}
	if (t->headers)
		curl_easy_setopt(t->easy, CURLOPT_HTTPHEADER, t->headers);
	curl_multi_add_handle(t->multi, t->easy);
	t->id = ++last_id;
	t->next = transfers;
	transfers = t;
	return t->id;
}

/*
 * http_start for Go: registering the transaction callback (on the first
 * request) allocates, which may raise an error.
 */
ErrorData *
gopgext_http_start(const char *method, const char *url, const char *headers,
				   const char *body, int64 body_len, int64 timeout_ms, char *errbuf,
				   int64 *id)
{
	*id = 0;
	GOPGEXT_GUARD(*id = http_start(method, url, headers, body, body_len, timeout_ms, errbuf));
}

/* Runs the transfer until cond holds or it ends. -2: an interrupt is pending. */
static int64
pump(transfer *t, bool (*cond) (transfer *))
{
	while (!cond(t) && !t->done)
	{
		int			running;
		CURLMcode	mc;

		if (INTERRUPTS_PENDING_CONDITION())
			return -2;
		mc = curl_multi_perform(t->multi, &running);
		if (mc != CURLM_OK)
		{
			snprintf(t->error, sizeof(t->error), "%s", curl_multi_strerror(mc));
			t->result = CURLE_RECV_ERROR;
			t->done = true;
			break;
		}
		if (!running)
		{
			CURLMsg    *msg;
			int			left;

			while ((msg = curl_multi_info_read(t->multi, &left)) != NULL)
				if (msg->msg == CURLMSG_DONE)
					t->result = msg->data.result;
			t->done = true;
			break;
		}
		if (!cond(t))
			curl_multi_poll(t->multi, NULL, 0, 100, NULL);
	}
	return 0;
}

static bool
has_headers(transfer *t)
{
	return t->hdr_done;
}

static bool
has_data(transfer *t)
{
	return t->len > t->off;
}

/*
 * Waits for the final response's headers. Returns 1, -1 on error
 * (gopgext_http_error), -2 if an interrupt is pending (call again after
 * processing it), -3 for an unknown (closed) transfer.
 */
int64
gopgext_http_wait_headers(int64 id)
{
	transfer   *t = lookup(id);

	if (!t)
		return -3;
	if (pump(t, has_headers) == -2)
		return -2;
	if (t->oom || (t->done && t->result != CURLE_OK))
		return -1;
	return 1;
}

/*
 * Waits for body data. Returns 1 with data and len set to the unconsumed
 * bytes, 0 at the end, -1 on error, -2 if an interrupt is pending, -3 for an
 * unknown transfer.
 */
int64
gopgext_http_read(int64 id, const char **data, int64 *len)
{
	transfer   *t = lookup(id);

	if (!t)
		return -3;
	if (pump(t, has_data) == -2)
		return -2;
	if (t->len > t->off)
	{
		*data = t->buf + t->off;
		*len = t->len - t->off;
		return 1;
	}
	if (t->oom || t->result != CURLE_OK)
		return -1;
	return 0;
}

void
gopgext_http_consume(int64 id, int64 n)
{
	transfer   *t = lookup(id);

	if (t)
		t->off += Min((size_t) n, t->len - t->off);
}

int64
gopgext_http_status(int64 id)
{
	transfer   *t = lookup(id);
	long		code = 0;

	if (t)
		curl_easy_getinfo(t->easy, CURLINFO_RESPONSE_CODE, &code);
	return code;
}

/* Header lines of the final response, including the status line. */
const char *
gopgext_http_headers(int64 id)
{
	transfer   *t = lookup(id);

	return t && t->hdr ? t->hdr : "";
}

const char *
gopgext_http_error(int64 id)
{
	transfer   *t = lookup(id);

	if (!t)
		return "the request was closed (it can't outlive its transaction)";
	if (t->oom)
		return "out of memory";
	if (t->error[0])
		return t->error;
	return curl_easy_strerror(t->result);
}

void
gopgext_http_close(int64 id)
{
	for (transfer **p = &transfers; *p; p = &(*p)->next)
		if ((*p)->id == id)
		{
			transfer   *t = *p;

			*p = t->next;
			free_transfer(t);
			return;
		}
}
