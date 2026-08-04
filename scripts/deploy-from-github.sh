#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

readonly DEFAULT_REPOSITORY='https://github.com/lane-cv/VAYZRA.git'
readonly DEFAULT_REF='master'
readonly DEFAULT_PROJECT='happylearn-dev'

repository=$DEFAULT_REPOSITORY
ref=$DEFAULT_REF
project=$DEFAULT_PROJECT
directory="$PWD/VAYZRA"
license_file=${HAPPYLEARN_AISTOR_LICENSE_FILE:-}
app_port=8080
internal_port=9090
postgres_port=54329
redis_port=56379
aistor_api_port=59000
aistor_console_port=59001

usage() {
  cat <<'EOF'
Usage: deploy-from-github.sh --license-file PATH [options]

Clone or fast-forward a clean HappyLearn checkout, build its images, and deploy
the local Docker Compose stack without deleting existing named volumes.

Options:
  --repository URL           GitHub clone URL
  --ref BRANCH               Branch to clone/update (default: master)
  --directory PATH           Checkout/deployment directory (default: ./VAYZRA)
  --project NAME             Compose project (default: happylearn-dev)
  --license-file PATH        Readable AIStor minio.license file (required)
  --app-port PORT            Web/API loopback port (default: 8080)
  --internal-port PORT       Internal API loopback port (default: 9090)
  --postgres-port PORT       PostgreSQL loopback port (default: 54329)
  --redis-port PORT          Redis loopback port (default: 56379)
  --aistor-api-port PORT     AIStor S3 loopback port (default: 59000)
  --aistor-console-port PORT AIStor console loopback port (default: 59001)
  -h, --help                 Show this help

This script is for local/development deployment. It does not perform the
approved Phase 6 production release protocol.
EOF
}

fail() {
  printf 'deploy-from-github: %s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

require_value() {
  (($# >= 2)) || fail "missing value for $1"
}

validate_port() {
  local label=$1 value=$2
  [[ $value =~ ^[0-9]+$ ]] || fail "$label must be a number"
  ((value >= 1024 && value <= 65535)) || fail "$label must be between 1024 and 65535"
}

secure_file() {
  local path=$1 label=$2 mode owner
  [[ -f $path && ! -L $path && -s $path ]] || fail "$label is missing, empty, or a symlink"
  owner=$(stat -c '%u' -- "$path") || fail "cannot inspect $label owner"
  mode=$(stat -c '%a' -- "$path") || fail "cannot inspect $label permissions"
  [[ $owner == "$(id -u)" ]] || fail "$label must be owned by the current user"
  (( (8#$mode & 8#077) == 0 )) || fail "$label must not be readable by group or others"
}

validate_ai_env() {
  local path=$1 version
  [[ $(wc -l <"$path" | tr -d '[:space:]') == 6 ]] || fail 'AI environment file has an invalid schema'
  [[ $(grep -Ec '^HAPPYLEARN_AI_MASTER_KEY=[A-Za-z0-9+/]{43}=$' "$path") == 1 ]] || fail 'AI environment file has an invalid master key'
  [[ $(grep -Ec '^HAPPYLEARN_AI_MASTER_KEY_VERSION=[1-9][0-9]*$' "$path") == 1 ]] || fail 'AI environment file has an invalid key version'
  version=$(sed -n 's/^HAPPYLEARN_AI_MASTER_KEY_VERSION=//p' "$path")
  ((version >= 1 && version <= 32767)) || fail 'AI environment key version is out of range'
  for expected in \
    'HAPPYLEARN_AI_BUSINESS_TIMEZONE=Asia/Shanghai' \
    'HAPPYLEARN_AI_GLOBAL_CONCURRENCY=2' \
    'HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY=1' \
    'HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=false'; do
    [[ $(grep -Fxc "$expected" "$path") == 1 ]] || fail 'AI environment file has an invalid schema'
  done
}

while (($#)); do
  case $1 in
    --repository|--ref|--directory|--project|--license-file|--app-port|--internal-port|--postgres-port|--redis-port|--aistor-api-port|--aistor-console-port)
      require_value "$@"
      case $1 in
        --repository) repository=$2 ;;
        --ref) ref=$2 ;;
        --directory) directory=$2 ;;
        --project) project=$2 ;;
        --license-file) license_file=$2 ;;
        --app-port) app_port=$2 ;;
        --internal-port) internal_port=$2 ;;
        --postgres-port) postgres_port=$2 ;;
        --redis-port) redis_port=$2 ;;
        --aistor-api-port) aistor_api_port=$2 ;;
        --aistor-console-port) aistor_console_port=$2 ;;
      esac
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *) fail "unknown argument: $1" ;;
  esac
done

for command in git docker openssl curl realpath stat mktemp install grep sed tr wc; do
  require_command "$command"
done
docker compose version >/dev/null 2>&1 || fail 'Docker Compose plugin is unavailable'

case $repository in
  https://github.com/*/*.git|git@github.com:*/*.git) ;;
  *) fail 'repository must be a GitHub HTTPS or SSH clone URL ending in .git' ;;
esac
[[ $repository != -* && $repository != *$'\n'* && $repository != *$'\r'* ]] || fail 'repository URL is unsafe'
git check-ref-format --branch "$ref" >/dev/null 2>&1 || fail 'ref must be a valid branch name'
[[ $project =~ ^[a-z0-9][a-z0-9_-]{0,62}$ ]] || fail 'project name is invalid'

validate_port app_port "$app_port"
validate_port internal_port "$internal_port"
validate_port postgres_port "$postgres_port"
validate_port redis_port "$redis_port"
validate_port aistor_api_port "$aistor_api_port"
validate_port aistor_console_port "$aistor_console_port"
declare -A seen_ports=()
for port in "$app_port" "$internal_port" "$postgres_port" "$redis_port" "$aistor_api_port" "$aistor_console_port"; do
  [[ -z ${seen_ports[$port]:-} ]] || fail "duplicate host port: $port"
  seen_ports[$port]=1
done

[[ -n $license_file ]] || fail '--license-file is required'
[[ $license_file != *$'\n'* && $license_file != *$'\r'* ]] || fail 'license path is unsafe'
license_file=$(realpath -e -- "$license_file") || fail 'license file does not exist'
[[ -f $license_file && ! -L $license_file && -r $license_file && -s $license_file ]] || fail 'license file is not a readable regular file'

if [[ $directory != /* ]]; then directory="$PWD/$directory"; fi
[[ $directory != / && $directory != "${HOME:-/nonexistent}" && ! -L $directory ]] || fail 'deployment directory is unsafe'
parent=$(dirname -- "$directory")
directory_name=$(basename -- "$directory")
[[ $directory_name != . && $directory_name != .. && $directory_name != *$'\n'* && $directory_name != *$'\r'* ]] || fail 'deployment directory is unsafe'
mkdir -p -- "$parent"
parent=$(realpath -e -- "$parent") || fail 'deployment directory parent is unavailable'
directory="$parent/$directory_name"

if [[ ! -e $directory ]]; then
  git clone --branch "$ref" --single-branch -- "$repository" "$directory"
else
  [[ -d $directory && ! -L $directory && -d $directory/.git ]] || fail 'deployment directory is not a Git checkout'
  [[ -z $(git -C "$directory" status --porcelain --untracked-files=normal) ]] || fail 'deployment checkout has uncommitted changes'
  origin_url=$(git -C "$directory" remote get-url origin) || fail 'deployment checkout has no origin remote'
  case $origin_url in
    https://github.com/*/*.git|git@github.com:*/*.git) ;;
    *) fail 'deployment checkout origin is not a GitHub clone URL' ;;
  esac
  current_branch=$(git -C "$directory" symbolic-ref --quiet --short HEAD) || fail 'deployment checkout is detached'
  [[ $current_branch == "$ref" ]] || fail "checkout branch is $current_branch, expected $ref"
  git -C "$directory" pull --ff-only origin "$ref"
fi

for required in deploy/compose.dev.yml deploy/compose.github.yml scripts/phase4-ai-operations.sh; do
  [[ -f $directory/$required && ! -L $directory/$required ]] || fail "checkout is missing $required"
done

secret_dir="$directory/.secrets/github-deploy"
base_env="$directory/.env.github-deploy"
ai_key="$secret_dir/ai-master-key"
ai_env="$secret_dir/ai.env"
install -d -m 0700 -- "$secret_dir"

if [[ -f $ai_env && ! -f $ai_key ]]; then
  fail 'AI environment exists but its master-key file is missing; refusing automatic key rotation'
fi
if [[ ! -f $ai_key ]]; then
  temporary_key=$(mktemp "$secret_dir/.ai-master-key.XXXXXX")
  trap 'rm -f -- "${temporary_key:-}" "${temporary_env:-}"' EXIT INT TERM HUP
  openssl rand -out "$temporary_key" -base64 32
  chmod 0600 "$temporary_key"
  mv -f -- "$temporary_key" "$ai_key"
  temporary_key=''
fi
secure_file "$ai_key" 'AI master-key file'
if [[ ! -f $ai_env ]]; then
  "$directory/scripts/phase4-ai-operations.sh" write-env "$ai_key" "$ai_env"
fi
secure_file "$ai_env" 'AI environment file'
validate_ai_env "$ai_env"

temporary_env=$(mktemp "$directory/.env.github-deploy.XXXXXX")
trap 'rm -f -- "${temporary_key:-}" "${temporary_env:-}"' EXIT INT TERM HUP
chmod 0600 "$temporary_env"
{
  printf 'HAPPYLEARN_APP_PORT=%s\n' "$app_port"
  printf 'HAPPYLEARN_INTERNAL_PORT=%s\n' "$internal_port"
  printf 'HAPPYLEARN_POSTGRES_PORT=%s\n' "$postgres_port"
  printf 'HAPPYLEARN_REDIS_PORT=%s\n' "$redis_port"
  printf 'HAPPYLEARN_AISTOR_API_PORT=%s\n' "$aistor_api_port"
  printf 'HAPPYLEARN_AISTOR_CONSOLE_PORT=%s\n' "$aistor_console_port"
  printf 'HAPPYLEARN_PUBLIC_ORIGIN=http://127.0.0.1:%s\n' "$app_port"
} >"$temporary_env"
mv -f -- "$temporary_env" "$base_env"
temporary_env=''
trap - EXIT INT TERM HUP

compose=(
  docker compose
  --project-name "$project"
  --project-directory "$directory"
  --env-file "$base_env"
  --env-file "$ai_env"
  -f "$directory/deploy/compose.dev.yml"
  -f "$directory/deploy/compose.github.yml"
)

export HAPPYLEARN_AISTOR_LICENSE_FILE=$license_file
"${compose[@]}" config --quiet
"${compose[@]}" up -d --build --wait --wait-timeout 300

ready_url="http://127.0.0.1:$app_port/api/v1/health/ready"
for _ in {1..30}; do
  if curl --fail --silent --show-error "$ready_url" >/dev/null 2>&1; then
    commit=$(git -C "$directory" rev-parse --short=12 HEAD)
    printf 'HappyLearn deployed successfully.\n'
    printf 'commit=%s\nproject=%s\nweb=http://127.0.0.1:%s\n' "$commit" "$project" "$app_port"
    printf 'aistor_console=http://127.0.0.1:%s\n' "$aistor_console_port"
    exit 0
  fi
  sleep 1
done

"${compose[@]}" ps >&2 || true
fail 'application readiness probe did not pass within 30 seconds'
