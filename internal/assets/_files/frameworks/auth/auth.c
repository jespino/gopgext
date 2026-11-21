/*
 * C side of gopgext/auth: ClientAuthentication_hook and check_password_hook,
 * reported to Go as events. The hooks are installed when Go registers a
 * callback, and chain to the previous ones (called first). An error Go
 * returns is raised once Go has returned: it rejects the connection or the
 * password.
 */
#include "postgres.h"

#include "commands/user.h"
#include "libpq/auth.h"
#include "libpq/crypt.h"
#include "libpq/hba.h"
#include "libpq/libpq-be.h"

#include "gopgext.h"

void		gopgext_go_auth_event(struct gopgext_call *call);

/* Event kinds (auth.go). */
enum
{
	AUTH_CONNECTION = 1,
	AUTH_PASSWORD = 2,
};

/* A connection being authenticated. Mirrored by auth.cconnection. */
struct auth_connection
{
	const char *user;
	const char *database;
	const char *application;
	const char *remote_host;
	const char *remote_port;
	const char *method;
	int64		status;			/* auth.Status */
};

/* A password being set. Mirrored by auth.cpassword. */
struct auth_password
{
	const char *role;
	const char *password;
	int64		type;			/* auth.PasswordType */
	int64		valid_until;	/* timestamptz */
	int64		has_valid_until;
};

void		gopgext_auth_install(int64 hooks);
ErrorData  *gopgext_auth_verify(const char *role, const char *shadow, const char *plain, bool *ok);

static ClientAuthentication_hook_type prev_client_auth_hook;
static check_password_hook_type prev_check_password_hook;
static int64 installed;

static void
call_go(int64 kind, void *event)
{
	LOCAL_FCINFO(fc, 2);

	InitFunctionCallInfoData(*fc, NULL, 2, InvalidOid, NULL, NULL);
	fc->args[0].value = Int64GetDatum(kind);
	fc->args[0].isnull = false;
	fc->args[1].value = PointerGetDatum(event);
	fc->args[1].isnull = false;
	gopgext_invoke(fc, gopgext_go_auth_event);
}

static void
auth_client_authentication(Port *port, int status)
{
	struct auth_connection c;

	if (prev_client_auth_hook)
		prev_client_auth_hook(port, status);
	if (!gopgext_loaded())
		return;
	c.user = port->user_name;
	c.database = port->database_name;
	c.application = port->application_name;
	c.remote_host = port->remote_host;
	c.remote_port = port->remote_port;
	c.method = port->hba ? hba_authname(port->hba->auth_method) : NULL;
	c.status = status == STATUS_OK ? 0 : status == STATUS_EOF ? 2 : 1;
	call_go(AUTH_CONNECTION, &c);
}

static void
auth_check_password(const char *username, const char *shadow_pass, PasswordType password_type,
					Datum validuntil_time, bool validuntil_null)
{
	struct auth_password p;

	if (prev_check_password_hook)
		prev_check_password_hook(username, shadow_pass, password_type, validuntil_time,
								 validuntil_null);
	if (!gopgext_loaded())
		return;
	p.role = username;
	p.password = shadow_pass;
	p.type = password_type == PASSWORD_TYPE_PLAINTEXT ? 0 : password_type == PASSWORD_TYPE_MD5 ? 1 : 2;
	p.valid_until = validuntil_null ? 0 : DatumGetInt64(validuntil_time);
	p.has_valid_until = !validuntil_null;
	call_go(AUTH_PASSWORD, &p);
}

/* Installs the hooks Go registers callbacks for (1: authentication, 2: passwords). */
void
gopgext_auth_install(int64 hooks)
{
	if ((hooks & 1) && !(installed & 1))
	{
		prev_client_auth_hook = ClientAuthentication_hook;
		ClientAuthentication_hook = auth_client_authentication;
	}
	if ((hooks & 2) && !(installed & 2))
	{
		prev_check_password_hook = check_password_hook;
		check_password_hook = auth_check_password;
	}
	installed |= hooks;
}

/* Whether plain is the (plain or encrypted) password shadow of role. */
ErrorData *
gopgext_auth_verify(const char *role, const char *shadow, const char *plain, bool *ok)
{
	GOPGEXT_GUARD({
		const char *logdetail = NULL;

		/* logdetail is char ** before PostgreSQL 16. */
		*ok = plain_crypt_verify(role, shadow, plain, (void *) &logdetail) == STATUS_OK;
	});
}
