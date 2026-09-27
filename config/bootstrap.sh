#!/usr/bin/env bash
# One-time Google Cloud setup for the Docs CLI.
#
# Automated: create the project if missing and enable the Docs and Drive APIs.
# Development also writes credentials.json once the Desktop client id and secret
# are in the env file. Production does not: docs-cli writes that file.
# Not automated: Google Auth platform has no stable CLI for a Desktop client.
# Branding, Audience (External / Testing / test user), Data Access, and Clients
# stay a one-time Console pass. IAP oauth-clients are a different client type
# and are intentionally not used here.
#
# ROOT is the parent of the script directory, not config/.
# Env file: APP_ENV=production reads ./.env; anything else reads ./.env.example.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

MODE=bootstrap
OPEN_BROWSER=0
for arg in "$@"; do
  case "$arg" in
    --check) MODE=check ;;
    --open) OPEN_BROWSER=1 ;;
    -h|--help)
      echo "usage: APP_ENV=development|production $0 [--check] [--open]"
      exit 0
      ;;
    *)
      echo "unknown argument: $arg" >&2
      exit 1
      ;;
  esac
done

load_env() {
  local file="$1" line key val
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z "$line" || "$line" == \#* ]] && continue
    if [[ "$line" == export\ * ]]; then
      line="${line#export }"
    fi
    key="${line%%=*}"
    val="${line#*=}"
    if [[ ! "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      echo "invalid env key in $file: $key" >&2
      exit 1
    fi
    if [[ "$val" == \"*\" && "$val" == *\" ]]; then
      val="${val:1:${#val}-2}"
    elif [[ "$val" == \'*\' && "$val" == *\' ]]; then
      val="${val:1:${#val}-2}"
    fi
    printf -v "$key" '%s' "$val"
    export "$key"
  done < "$file"
}

require() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    echo "missing $name in $ENV_FILE" >&2
    exit 1
  fi
}

APP_ENV_FROM_USER="${APP_ENV:-}"
ENV_FILE="$ROOT/.env.example"
if [[ "$APP_ENV_FROM_USER" == "production" ]]; then
  ENV_FILE="$ROOT/.env"
fi
if [[ ! -f "$ENV_FILE" ]]; then
  echo "missing $ENV_FILE" >&2
  if [[ "$APP_ENV_FROM_USER" == "production" ]]; then
    echo "production reads .env. Copy .env.example to .env and fill the client secret there." >&2
  fi
  exit 1
fi

load_env "$ENV_FILE"
if [[ -n "$APP_ENV_FROM_USER" ]]; then
  APP_ENV="$APP_ENV_FROM_USER"
fi
APP_ENV="${APP_ENV:-development}"
if [[ "$APP_ENV" != "development" && "$APP_ENV" != "production" ]]; then
  echo "APP_ENV must be development or production" >&2
  exit 1
fi

require GCP_PROJECT_ID
require GCP_PROJECT_NAME
require GOOGLE_DOCS_API
require GOOGLE_DRIVE_API
require GOOGLE_OAUTH_APP_NAME
require GOOGLE_OAUTH_SUPPORT_EMAIL
require GOOGLE_OAUTH_USER_TYPE
require GOOGLE_OAUTH_PUBLISHING_STATUS
require GOOGLE_OAUTH_TEST_USER
require GOOGLE_OAUTH_CLIENT_NAME
require GOOGLE_OAUTH_CLIENT_TYPE
require GOOGLE_OAUTH_AUTH_URI
require GOOGLE_OAUTH_TOKEN_URI
require GOOGLE_OAUTH_CERT_URL
require GOOGLE_OAUTH_REDIRECT_URI
require GOOGLE_OAUTH_SCOPE
require GOOGLE_OAUTH_ACCESS_TYPE
require GOOGLE_OAUTH_PROMPT
require GOOGLE_OAUTH_PKCE_METHOD
require OAUTH_TIMEOUT
require GOOGLE_CREDENTIALS_PATH
require DOCS_INSERT_INDEX
require DOCS_DEFAULT_TITLE

if [[ "$GOOGLE_OAUTH_CLIENT_TYPE" != "installed" ]]; then
  echo "GOOGLE_OAUTH_CLIENT_TYPE must be installed (Desktop app)" >&2
  exit 1
fi
if [[ "$GOOGLE_OAUTH_USER_TYPE" != "external" || "$GOOGLE_OAUTH_PUBLISHING_STATUS" != "testing" ]]; then
  echo "expected external + testing consent screen" >&2
  exit 1
fi
if [[ "$DOCS_INSERT_INDEX" != "1" ]]; then
  echo "DOCS_INSERT_INDEX must be 1 for a new blank document" >&2
  exit 1
fi
if [[ "$GOOGLE_OAUTH_SCOPE" == *readonly* ]]; then
  echo "GOOGLE_OAUTH_SCOPE cannot be a readonly scope" >&2
  exit 1
fi
if [[ "$GOOGLE_OAUTH_SCOPE" != "https://www.googleapis.com/auth/drive" ]]; then
  echo "GOOGLE_OAUTH_SCOPE must be https://www.googleapis.com/auth/drive" >&2
  exit 1
fi
if [[ -n "$GCP_ORGANIZATION_ID" && -n "$GCP_FOLDER_ID" ]]; then
  echo "set only one of GCP_ORGANIZATION_ID or GCP_FOLDER_ID" >&2
  exit 1
fi
if [[ "$GOOGLE_DOCS_API" != "docs.googleapis.com" ]]; then
  echo "GOOGLE_DOCS_API must be docs.googleapis.com" >&2
  exit 1
fi
if [[ "$GOOGLE_DRIVE_API" != "drive.googleapis.com" ]]; then
  echo "GOOGLE_DRIVE_API must be drive.googleapis.com" >&2
  exit 1
fi

has_client=0
if [[ -n "${GOOGLE_OAUTH_CLIENT_ID:-}" && -n "${GOOGLE_OAUTH_CLIENT_SECRET:-}" ]]; then
  has_client=1
  if [[ "$GOOGLE_OAUTH_CLIENT_ID" != *.apps.googleusercontent.com ]]; then
    echo "GOOGLE_OAUTH_CLIENT_ID does not look like a Google OAuth client id" >&2
    exit 1
  fi
fi

BRANDING_URL="https://console.cloud.google.com/auth/branding?project=${GCP_PROJECT_ID}"
AUDIENCE_URL="https://console.cloud.google.com/auth/audience?project=${GCP_PROJECT_ID}"
DATA_ACCESS_URL="https://console.cloud.google.com/auth/scopes?project=${GCP_PROJECT_ID}"
CLIENTS_URL="https://console.cloud.google.com/auth/clients?project=${GCP_PROJECT_ID}"
DOCS_LIBRARY_URL="https://console.cloud.google.com/apis/library/docs.googleapis.com?project=${GCP_PROJECT_ID}"
DRIVE_LIBRARY_URL="https://console.cloud.google.com/apis/library/drive.googleapis.com?project=${GCP_PROJECT_ID}"

echo "env=$APP_ENV file=$(basename "$ENV_FILE") project=$GCP_PROJECT_ID"

if [[ "$MODE" == "check" ]]; then
  if [[ "$has_client" == "1" ]]; then
    echo "oauth_client=present"
  else
    echo "oauth_client=missing"
    echo "next: branding  $BRANDING_URL"
    echo "next: audience  $AUDIENCE_URL"
    echo "next: data-access  $DATA_ACCESS_URL"
    echo "next: clients   $CLIENTS_URL"
  fi
  echo "insert_index=$DOCS_INSERT_INDEX scope=$GOOGLE_OAUTH_SCOPE"
  echo "apis=docs.googleapis.com,drive.googleapis.com"
  exit 0
fi

if ! command -v gcloud >/dev/null 2>&1; then
  echo "gcloud is not installed; project setup was not run" >&2
  exit 1
fi

if ! gcloud projects describe "$GCP_PROJECT_ID" >/dev/null 2>&1; then
  create_args=(projects create "$GCP_PROJECT_ID" --name="$GCP_PROJECT_NAME")
  if [[ -n "$GCP_FOLDER_ID" ]]; then
    create_args+=(--folder="$GCP_FOLDER_ID")
  elif [[ -n "$GCP_ORGANIZATION_ID" ]]; then
    create_args+=(--organization="$GCP_ORGANIZATION_ID")
  fi
  gcloud "${create_args[@]}"
fi

gcloud config set project "$GCP_PROJECT_ID" >/dev/null
if ! gcloud services enable docs.googleapis.com --project="$GCP_PROJECT_ID"; then
  echo "$DOCS_LIBRARY_URL" >&2
  echo "$DRIVE_LIBRARY_URL" >&2
  exit 1
fi
if ! gcloud services enable drive.googleapis.com --project="$GCP_PROJECT_ID"; then
  echo "$DOCS_LIBRARY_URL" >&2
  echo "$DRIVE_LIBRARY_URL" >&2
  exit 1
fi

if [[ -n "$GCP_BILLING_ACCOUNT" ]]; then
  gcloud billing projects link "$GCP_PROJECT_ID" --billing-account="$GCP_BILLING_ACCOUNT"
fi

if [[ "$has_client" == "0" ]]; then
  echo "project ready. Desktop client is still manual:"
  echo "1. Branding: name=$GOOGLE_OAUTH_APP_NAME support=$GOOGLE_OAUTH_SUPPORT_EMAIL"
  echo "   $BRANDING_URL"
  echo "2. Audience: $GOOGLE_OAUTH_USER_TYPE / $GOOGLE_OAUTH_PUBLISHING_STATUS, test user $GOOGLE_OAUTH_TEST_USER"
  echo "   $AUDIENCE_URL"
  echo "3. Data Access: add only $GOOGLE_OAUTH_SCOPE"
  echo "   $DATA_ACCESS_URL"
  echo "4. Clients: create Desktop app named $GOOGLE_OAUTH_CLIENT_NAME"
  echo "   $CLIENTS_URL"
  if [[ "$APP_ENV" == "production" ]]; then
    echo "5. Put the client id and secret into $(basename "$ENV_FILE"), then run ./docs-cli setup again."
  else
    echo "5. Put the client id and secret into $(basename "$ENV_FILE"), then rerun this script."
  fi
  echo "   The browser consent flow does not create the client id. Run docs-cli auth after the id and secret are stored."
  echo "API pages: $DOCS_LIBRARY_URL"
  echo "           $DRIVE_LIBRARY_URL"
  if [[ "$OPEN_BROWSER" == "1" ]]; then
    if command -v xdg-open >/dev/null 2>&1; then
      xdg-open "$CLIENTS_URL" >/dev/null 2>&1 || true
    fi
  fi
  exit 0
fi

if [[ "$has_client" == "1" && "$APP_ENV" == "production" ]]; then
  echo "project ready. production credentials.json is written by docs-cli, not this script."
  exit 0
fi

if [[ "$APP_ENV" == "development" ]]; then
  echo "warning: client secret is in .env.example; do not commit that value" >&2
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required to write credentials.json" >&2
  exit 1
fi

cred_dir="$(dirname "$GOOGLE_CREDENTIALS_PATH")"
mkdir -p "$cred_dir"
umask 077
python3 - "$GOOGLE_CREDENTIALS_PATH" <<'PY'
import json, os, sys
path = sys.argv[1]
doc = {
    "installed": {
        "client_id": os.environ["GOOGLE_OAUTH_CLIENT_ID"],
        "project_id": os.environ["GCP_PROJECT_ID"],
        "auth_uri": os.environ["GOOGLE_OAUTH_AUTH_URI"],
        "token_uri": os.environ["GOOGLE_OAUTH_TOKEN_URI"],
        "auth_provider_x509_cert_url": os.environ["GOOGLE_OAUTH_CERT_URL"],
        "client_secret": os.environ["GOOGLE_OAUTH_CLIENT_SECRET"],
        "redirect_uris": [os.environ["GOOGLE_OAUTH_REDIRECT_URI"]],
    }
}
with open(path, "w", encoding="utf-8") as fh:
    json.dump(doc, fh, indent=2)
    fh.write("\n")
os.chmod(path, 0o600)
PY

echo "wrote $GOOGLE_CREDENTIALS_PATH"
echo "refresh token: keyring item docs-cli / refresh-token (not a file)"
echo "blank doc insert index: $DOCS_INSERT_INDEX"
