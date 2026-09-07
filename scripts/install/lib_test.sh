#!/usr/bin/env bash
#
# scripts/install/lib_test.sh
# ============================
#
# Smoke tests for lib.sh's pure-logic helpers: install_mode_dir,
# require_sudo's failure mode, binary_name_for (headless + computeruse
# flavours), and write_worker_yaml (fresh write, 0600 mode, capability
# list, and its --force clobber guard). The /usr/local/bin system path
# can't be exercised in CI without elevated privileges, so the
# system-mode path is checked via the require_sudo failure mode.
# Also exercises the installers' own lib.sh sourcing: the cloned-repo
# sibling path, and the curl|bash fallback that fetches lib.sh from
# MEMQL_INSTALL_RAW_BASE when no sibling exists.
# And the release-asset preflight: preflight_asset's verdicts over
# file:// fixtures (present passes, missing exits 4 naming the URL and
# the flavour/platform pair), plus both installers refusing BEFORE any
# mutation when the asset is missing and proceeding past the preflight
# to the download when it is present.
# And the --inference pass-through: setup_inference's reading of the
# setup command's exit code (0 quiet, 3 prints the interactive command
# on ONE physical line, anything else reported), that it never fails the
# install, and that both installers accept the flag and call it after
# worker.yaml and the service.
#
# Run: bash scripts/install/lib_test.sh
# Wired into CI by .github/workflows/install-scripts-lint.yml.

set -uo pipefail

# Source lib.sh; suppress its `set -uo pipefail` from killing this
# driver if a helper returns non-zero (we assert that here).
# shellcheck disable=SC1091
source "$(dirname "$0")/lib.sh"

PASS=0
FAIL=0

function fail() {
    echo "FAIL: $*" >&2
    FAIL=$((FAIL + 1))
}

function pass() {
    echo "PASS: $*"
    PASS=$((PASS + 1))
}

function expect_eq() {
    local name="$1"
    local got="$2"
    local want="$3"
    if [[ "$got" == "$want" ]]; then
        pass "$name"
    else
        fail "$name: got=$got want=$want"
    fi
}

# ---------------------------------------------------------------
# install_mode_dir
# ---------------------------------------------------------------

expect_eq "install_mode_dir system" "$(install_mode_dir system)" "/usr/local/bin"
expect_eq "install_mode_dir user-local" "$(install_mode_dir user-local)" "${HOME}/.memql/bin"

# Unknown mode prints to stderr + non-zero.
if install_mode_dir bogus >/dev/null 2>&1; then
    fail "install_mode_dir bogus accepted; should have errored"
else
    pass "install_mode_dir bogus rejected"
fi

# ---------------------------------------------------------------
# require_sudo behaviour
# ---------------------------------------------------------------

# Running as root would short-circuit; in CI / dev we're not root.
if [[ $EUID -eq 0 ]]; then
    echo "INFO: running as root -- skipping require_sudo failure-mode tests"
else
    # Force sudo to be unreachable by clearing PATH inside a subshell
    # (so `command -v sudo` and sudo itself both fail). Running the
    # subshell as the `if` condition checks its status directly.
    if (
        # shellcheck disable=SC2123  # clearing PATH is the point of this probe
        PATH=""
        require_sudo
    ) >/dev/null 2>&1; then
        fail "require_sudo passed with PATH cleared (expected fail)"
    else
        pass "require_sudo fails when sudo unreachable"
    fi
fi

# ---------------------------------------------------------------
# binary_name_for
# ---------------------------------------------------------------

_os="$(detect_os)"
_arch="$(detect_arch)"

expect_eq "binary_name_for headless" \
    "$(binary_name_for headless)" "${INSTALLED_COMMAND}-${_os}-${_arch}"
expect_eq "binary_name_for computeruse" \
    "$(binary_name_for computeruse)" "${INSTALLED_COMMAND}-computeruse-${_os}-${_arch}"

# Unknown flavour prints to stderr + non-zero.
if binary_name_for bogus >/dev/null 2>&1; then
    fail "binary_name_for bogus accepted; should have errored"
else
    pass "binary_name_for bogus rejected"
fi

# ---------------------------------------------------------------
# write_worker_yaml
# ---------------------------------------------------------------

_tmp="$(mktemp -d)"
trap 'rm -rf "$_tmp"' EXIT
_wy="${_tmp}/worker.yaml"

# file_mode prints an octal permission string portably (GNU + BSD stat).
function file_mode() {
    stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1" 2>/dev/null
}

# Fresh write with default capabilities (HEADLESS only).
if write_worker_yaml "$_wy" "https://c.example" "mql_wkr_abc" "host1" "no" >/dev/null 2>&1; then
    pass "write_worker_yaml fresh write succeeds"
else
    fail "write_worker_yaml fresh write should succeed"
fi

expect_eq "write_worker_yaml renders mode 0600" "$(file_mode "$_wy")" "600"

if grep -qx '  - HEADLESS' "$_wy"; then
    pass "write_worker_yaml default lists HEADLESS capability"
else
    fail "write_worker_yaml default should list HEADLESS capability"
fi

# COMPUTERUSE appears in the concurrency block, so match the capability
# LIST line exactly (whole line) rather than a substring.
if grep -qx '  - COMPUTERUSE' "$_wy"; then
    fail "write_worker_yaml default must not list COMPUTERUSE capability"
else
    pass "write_worker_yaml default omits COMPUTERUSE capability"
fi

# Refuses to clobber an existing file without force.
if write_worker_yaml "$_wy" "https://c.example" "mql_wkr_abc" "host1" "no" >/dev/null 2>&1; then
    fail "write_worker_yaml clobbered an existing file without --force"
else
    pass "write_worker_yaml refuses to clobber without --force"
fi

# Overwrites with force=yes and the computer-use capability set.
if write_worker_yaml "$_wy" "https://c.example" "mql_wkr_abc" "host1" "yes" "HEADLESS,COMPUTERUSE" >/dev/null 2>&1; then
    pass "write_worker_yaml overwrites with --force"
else
    fail "write_worker_yaml should overwrite with --force"
fi

if grep -qx '  - HEADLESS' "$_wy" && grep -qx '  - COMPUTERUSE' "$_wy"; then
    pass "write_worker_yaml computeruse lists HEADLESS + COMPUTERUSE"
else
    fail "write_worker_yaml computeruse should list HEADLESS + COMPUTERUSE"
fi

# ---------------------------------------------------------------
# install.sh restates lib.sh's names -- they must agree
# ---------------------------------------------------------------
#
# The root install.sh is fetched ALONE by `curl | sh` and has no lib.sh to
# source, so it carries its own copy of the migration names. A rename
# applied to one file and not the other produces an installer that retires
# a service nobody registered, or registers one nothing will find -- both
# silent. Reading the literals out of the script is what makes the
# duplication safe.

_install_sh="$(dirname "$0")/../../install.sh"

function install_sh_value() {
    local key="$1"
    grep -E "^${key}=" "$_install_sh" | head -1 | sed -E "s/^${key}=\"?([^\"]*)\"?.*/\\1/"
}

if [[ -f "$_install_sh" ]]; then
    expect_eq "install.sh installs the same command name" \
        "$(install_sh_value BINARY)" "$INSTALLED_COMMAND"
    expect_eq "install.sh SERVICE_LABEL_DARWIN" \
        "$(install_sh_value SERVICE_LABEL_DARWIN)" "com.znasllc.memql-worker"
    expect_eq "install.sh SERVICE_LABEL_LINUX" \
        "$(install_sh_value SERVICE_LABEL_LINUX)" "memql-worker"
    expect_eq "install.sh LEGACY_LABEL_DARWIN" \
        "$(install_sh_value LEGACY_LABEL_DARWIN)" "com.znasllc.memql-cockpit-worker"
    expect_eq "install.sh LEGACY_LABEL_LINUX" \
        "$(install_sh_value LEGACY_LABEL_LINUX)" "memql-cockpit-worker"

    # The new label must not BE the old one -- a migration that renames
    # nothing would satisfy every other assertion here.
    if [[ "$(install_sh_value SERVICE_LABEL_DARWIN)" == "$(install_sh_value LEGACY_LABEL_DARWIN)" ]]; then
        fail "install.sh's darwin service label equals the legacy one; it migrates nothing"
    else
        pass "install.sh darwin service label differs from the legacy label"
    fi
else
    fail "install.sh not found at $_install_sh"
fi

# ---------------------------------------------------------------
# Installer sourcing -- cloned repo vs curl|bash (piped stdin)
# ---------------------------------------------------------------
#
# The installers source lib.sh from $(dirname "$0"), which under
# `curl ... | bash` is the operator's cwd ($0 is `bash`), so they fall
# back to fetching lib.sh from MEMQL_INSTALL_RAW_BASE. That override is
# what keeps these tests offline: the real lib.sh in this directory IS
# the fixture, reached over file://, and the failure case points at a
# path that cannot resolve. `--help` is the probe because it exits 0
# before any download / sudo / service work.

_script_dir="$(cd "$(dirname "$0")" && pwd)"
_piped_cwd="${_tmp}/piped-cwd"
mkdir -p "$_piped_cwd"

for _installer in install-mac.sh install-linux.sh; do
    # Piped from a cwd holding no lib.sh, with RAW_BASE at the real
    # lib.sh: must survive sourcing and reach flag handling.
    if _out="$(cd "$_piped_cwd" && MEMQL_INSTALL_RAW_BASE="file://${_script_dir}" \
        bash -s -- --help < "${_script_dir}/${_installer}" 2>&1)" \
        && [[ "$_out" == *"Usage:"* ]]; then
        pass "$_installer piped --help sources lib.sh from RAW_BASE"
    else
        fail "$_installer piped --help should print usage and exit 0; got: $_out"
    fi

    # The cloned-repo path must NEVER fetch: with RAW_BASE poisoned,
    # running next to lib.sh still works because the sibling wins.
    if _out="$(cd "$_script_dir" && MEMQL_INSTALL_RAW_BASE="file:///nonexistent-raw-base" \
        "./${_installer}" --help 2>&1)" && [[ "$_out" == *"Usage:"* ]]; then
        pass "$_installer cloned-repo --help never consults RAW_BASE"
    else
        fail "$_installer cloned-repo --help should use the sibling lib.sh; got: $_out"
    fi

    # Piped with a broken RAW_BASE: must die with an ERROR naming the
    # lib.sh URL it tried, not a cryptic bash sourcing error.
    if _out="$(cd "$_piped_cwd" && MEMQL_INSTALL_RAW_BASE="file://${_tmp}/no-such-dir" \
        bash -s -- --help < "${_script_dir}/${_installer}" 2>&1)"; then
        fail "$_installer piped with broken RAW_BASE should fail; got: $_out"
    elif [[ "$_out" == *"ERROR"* && "$_out" == *"file://${_tmp}/no-such-dir/lib.sh"* ]]; then
        pass "$_installer piped fetch failure names the lib.sh URL"
    else
        fail "$_installer piped fetch failure should name the URL it tried; got: $_out"
    fi
done

# ---------------------------------------------------------------
# preflight_asset -- refuse a missing release asset before mutating
# ---------------------------------------------------------------
#
# The helper alone first: verdicts over file:// (present -> 0, missing
# -> 4), which is what keeps these checks offline. file:// is also why
# the helper carries its ranged-GET fallback -- HEAD support for the
# FILE protocol varies by curl build -- so passing here proves the
# fallback chain, not just `curl -I`.

_pf_assets="${_tmp}/preflight-assets"
mkdir -p "$_pf_assets"
_pf_headless="$(binary_name_for headless)"
_pf_computeruse="$(binary_name_for computeruse)"
printf 'stub-binary' > "${_pf_assets}/${_pf_headless}"

if preflight_asset "file://${_pf_assets}/${_pf_headless}" headless >/dev/null 2>&1; then
    pass "preflight_asset passes a present asset"
else
    fail "preflight_asset should pass a present asset"
fi

_out="$(preflight_asset "file://${_pf_assets}/${_pf_computeruse}" computeruse 2>&1)"
_rc=$?
expect_eq "preflight_asset missing asset returns 4 (prerequisite missing)" "$_rc" "4"

if [[ "$_out" == *"file://${_pf_assets}/${_pf_computeruse}"* ]]; then
    pass "preflight_asset refusal names the exact asset URL"
else
    fail "preflight_asset refusal should name the asset URL; got: $_out"
fi

if [[ "$_out" == *"flavour: computeruse"* && "$_out" == *"${_os}/${_arch}"* ]]; then
    pass "preflight_asset refusal names flavour + platform"
else
    fail "preflight_asset refusal should name flavour + os/arch; got: $_out"
fi

# ---------------------------------------------------------------
# Installer preflight -- refusal mutates nothing; presence passes
# ---------------------------------------------------------------
#
# Now the call site: both installers must refuse AT the preflight --
# exit 4, nothing created under HOME, no download attempted -- and a
# present asset must sail through it. --user-local + --no-service keep
# the runs sudo-free; a fresh HOME per run is what makes "nothing was
# created" assertable. The success run still fails LATER, by design:
# download_binary pins --proto '=https' and so refuses the file://
# fixture. That is fine -- the assertion here is progress PAST the
# preflight (the download attempt on the same URL), not a completed
# install.

for _installer in install-mac.sh install-linux.sh; do
    # Missing asset (--computeruse against a base that publishes
    # nothing): a clean refusal before any mutation.
    _pf_home="${_tmp}/pf-404-home-${_installer}"
    mkdir -p "$_pf_home"
    _out="$(cd "$_script_dir" && HOME="$_pf_home" "./${_installer}" \
        --token mql_wkr_test --cluster https://c.example --computeruse \
        --user-local --no-service \
        --download-base "file://${_pf_assets}-none" 2>&1)"
    _rc=$?
    expect_eq "$_installer missing asset exits 4" "$_rc" "4"

    if [[ "$_out" == *"file://${_pf_assets}-none/${_pf_computeruse}"* \
        && "$_out" == *"flavour: computeruse"* && "$_out" == *"${_os}/${_arch}"* ]]; then
        pass "$_installer refusal names the asset URL + flavour/platform"
    else
        fail "$_installer refusal should name URL + flavour/platform; got: $_out"
    fi

    if [[ ! -e "${_pf_home}/.memql" && ! -e "${_pf_home}/Library" \
        && ! -e "${_pf_home}/.config" && "$_out" != *"INFO: downloading"* ]]; then
        pass "$_installer refusal mutates nothing (no ~/.memql, no service dir, no download)"
    else
        fail "$_installer refusal left state behind or attempted a download"
    fi

    # Present asset (headless at the fixture base): the preflight
    # passes and the install proceeds to the download of the SAME URL.
    _pf_home_ok="${_tmp}/pf-ok-home-${_installer}"
    mkdir -p "$_pf_home_ok"
    _out="$(cd "$_script_dir" && HOME="$_pf_home_ok" "./${_installer}" \
        --token mql_wkr_test --cluster https://c.example \
        --user-local --no-service \
        --download-base "file://${_pf_assets}" 2>&1)"
    _rc=$?
    if [[ "$_rc" != "4" && "$_out" != *"release asset not found"* ]]; then
        pass "$_installer present asset passes preflight"
    else
        fail "$_installer present asset should pass preflight; rc=$_rc got: $_out"
    fi

    if [[ "$_out" == *"INFO: downloading file://${_pf_assets}/${_pf_headless}"* ]]; then
        pass "$_installer proceeds past preflight to the download"
    else
        fail "$_installer should reach the download after preflight; got: $_out"
    fi
done

# ---------------------------------------------------------------
# setup_inference -- the --inference pass-through
# ---------------------------------------------------------------
#
# A machine that paired fine and could not set up local models is still
# a working worker, so setup_inference REPORTS every failure and returns
# 0. Exit 3 is the one with its own answer -- "refused: required
# confirmation not provided", which here means a runtime install a
# scripted run may not approve -- and the answer is the interactive
# command, printed for the person who is standing at this terminal now.
# A fake binary stands in for memql: what is under test is the shell's
# reading of `$?`, not the Go program's.

_si_bin="${_tmp}/fake-memql"
_si_log="${_tmp}/fake-memql.argv"
cat > "$_si_bin" << STUB
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "${_si_log}"
exit "\${FAKE_MEMQL_EXIT:-0}"
STUB
chmod +x "$_si_bin"

_out="$(FAKE_MEMQL_EXIT=0 setup_inference "$_si_bin" 2>&1)"
_rc=$?
expect_eq "setup_inference success returns 0" "$_rc" "0"
expect_eq "setup_inference runs the non-interactive setup" \
    "$(tail -1 "$_si_log")" "worker setup --inference --non-interactive"

# Exit 3: print the interactive command, and do not fail the install.
_out="$(FAKE_MEMQL_EXIT=3 setup_inference "$_si_bin" 2>&1)"
_rc=$?
expect_eq "setup_inference exit 3 still returns 0 (the install succeeded)" "$_rc" "0"

# The command MUST BE ONE PHYSICAL LINE: it is meant to be copied out of
# the terminal, and a bracketed paste of a wrapped command has already
# cost this project once. grep -qx matches the WHOLE line.
if printf '%s\n' "$_out" | grep -qx "  ${_si_bin} worker setup --inference"; then
    pass "setup_inference exit 3 prints the interactive command on one line"
else
    fail "setup_inference exit 3 should print the interactive command; got: $_out"
fi

if [[ "$_out" == *"worker setup --inference --non-interactive"* ]]; then
    fail "setup_inference exit 3 must not tell the operator to re-run the flag that refused"
else
    pass "setup_inference exit 3 drops --non-interactive from the printed command"
fi

# Any other non-zero: reported, install not failed. A below-floor
# machine (4) or a failed pull (5) is not a reason to undo a working
# pairing.
for _si_code in 4 5 1; do
    _out="$(FAKE_MEMQL_EXIT="$_si_code" setup_inference "$_si_bin" 2>&1)"
    _rc=$?
    expect_eq "setup_inference exit ${_si_code} still returns 0" "$_rc" "0"
    if [[ "$_out" == *"WARN"* && "$_out" == *"exited ${_si_code}"* \
        && "$_out" == *"paired and working"* ]]; then
        pass "setup_inference exit ${_si_code} reports without failing the install"
    else
        fail "setup_inference exit ${_si_code} should report the code and say the machine still works; got: $_out"
    fi
done

# ---------------------------------------------------------------
# Both installers accept --inference and wire it after the service
# ---------------------------------------------------------------
#
# The flag itself is checked by running the real installer: --help exits
# 0 before any download, so `--inference --help` proves the flag reached
# a case arm rather than the unknown-flag branch. The CALL SITE is
# checked by reading main(), because the end-to-end path needs a
# release asset over https and these tests stay offline.

for _installer in install-mac.sh install-linux.sh; do
    if _out="$(cd "$_script_dir" && "./${_installer}" --inference --help 2>&1)" \
        && [[ "$_out" == *"Usage:"* ]]; then
        pass "$_installer accepts --inference"
    else
        fail "$_installer should accept --inference; got: $_out"
    fi

    if [[ "$_out" == *"--inference"* ]]; then
        pass "$_installer documents --inference in its help"
    else
        fail "$_installer help should document --inference"
    fi

    # The order matters: worker.yaml first (nothing to configure
    # without it), the service next (so there is a running worker for
    # the setup's SIGHUP to reach), setup_inference last.
    _line_cfg="$(grep -nF '    write_config' "${_script_dir}/${_installer}" | head -1 | cut -d: -f1)"
    _line_inf="$(grep -nF 'setup_inference "' "${_script_dir}/${_installer}" | head -1 | cut -d: -f1)"
    if [[ -n "$_line_cfg" && -n "$_line_inf" && "$_line_inf" -gt "$_line_cfg" ]]; then
        pass "$_installer runs setup_inference after worker.yaml is written"
    else
        fail "$_installer should call setup_inference after write_config (cfg=$_line_cfg inf=$_line_inf)"
    fi

    # And only when asked: a plain install must not touch the runtime.
    if grep -qF 'INFERENCE" == "yes"' "${_script_dir}/${_installer}"; then
        pass "$_installer only sets up inference when --inference was given"
    else
        fail "$_installer should guard setup_inference behind --inference"
    fi
done

# ---------------------------------------------------------------
# Summary
# ---------------------------------------------------------------

echo ""
echo "===================="
echo "PASS: $PASS"
echo "FAIL: $FAIL"
echo "===================="

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
exit 0
