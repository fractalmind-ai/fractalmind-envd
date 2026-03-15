# KR6 Runbook: Non-Self Target Node Control Admission

## Goal

Close the remaining KR6 gap with one reviewable evidence chain:

1. the target node is registered under the correct **SuLabs org**
2. the admin-side coordinator can see that target node in `/api/sentinels`
3. the admin-side coordinator can execute a real remote `status`
4. the admin-side coordinator can execute a real remote `shell`, `logs`, or `tmux send-keys`
5. the result is clearly **not self-loop**, but landed on the intended target node

This runbook assumes `latest-main command-plane runtime proof` is already `PASS`.

## Fixed Baseline

Use these exact values unless the owner explicitly updates them:

- network: `testnet`
- RPC: `https://fullnode.testnet.sui.io:443`
- `envd package_id`: `0x74aef8ff3bb0da5d5626780e6c0e5f1f36308a40580e519920fdc9204e73d958`
- `protocol_package_id`: `0x685d6fb6ed8b0e679bb467ea73111819ec6ff68b1466d24ca26b400095dcdf24`
- `registry_id`: `0xe557465293df033fd6ba1347706d7e9db2a35de4667a3b6a2e20252587b6e505`
- expected `org_id`: `0x9f6e36a1e0fe240b429ac1278f8b76d2d3ef13efd0f8f4863f9cf33d967b157f`

## Most Likely Source Of Current Org Mismatch

Based on Discussion `#22`, the most likely local source is:

- `cert_id` is left empty, so `envd` auto-discovers an existing `AgentCertificate`
- the local `sui.key` belongs to a signer that already owns a certificate in a different org
- startup then reuses that historical certificate instead of a SuLabs-org certificate

That means `org_id` in `sentinel.yaml` can look correct while the effective cert still belongs to the wrong org.

So for Trinity / RoseX, do **not** treat config text alone as sufficient. Always verify the actual discovered `cert_id` and its on-chain `org_id`.

## Preconditions

Before starting:

- coordinator node is on `latest main`
- target node is on `latest main`
- target node is configured with the SuLabs testnet values above
- you know:
  - admin-side command-plane endpoint, for example `http://<coordinator-host>:8080`
  - coordinator log path
  - target node log path

## Step 1: Confirm Target Registration Identity

### 1.1 Capture signer and discovered certificate

On the target node:

```bash
grep -E '\[sui\] address=|\[sui\] found existing AgentCertificate:|\[sui\] peer registered on-chain' <target-envd-log>
```

Pass:

- shows signer address
- shows discovered or created `AgentCertificate`
- shows `[sui] peer registered on-chain`

Fail:

- no signer line
- no cert line
- no registration line

If failed, capture:

```bash
sed -n '/^sui:/,/^[^[:space:]]/p' <target-sentinel.yaml>
sed -n '/^gateway:/,/^[^[:space:]]/p' <target-sentinel.yaml>
sed -n '/^coordinator:/,/^[^[:space:]]/p' <target-sentinel.yaml>
```

### 1.2 Confirm the cert belongs to the SuLabs org

If the previous step showed a cert id, inspect it:

```bash
sui client object <cert-id> --json
```

Pass:

- object type is `0x685d6fb6ed8b0e679bb467ea73111819ec6ff68b1466d24ca26b400095dcdf24::agent::AgentCertificate`
- object content shows `org_id = 0x9f6e36a1e0fe240b429ac1278f8b76d2d3ef13efd0f8f4863f9cf33d967b157f`

Fail:

- `org_id` is different
- cert does not exist
- cert belongs to the right signer but the wrong org

If the cert is wrong, stop calling the target “ready for control admission”. Fix cert selection first.

## Step 2: Confirm Admin-Side Coordinator Is Up

Set:

```bash
export COORD=http://<coordinator-host>:<port>
```

### 2.1 Health check

```bash
curl -sS "$COORD/api/health"
```

Pass:

- JSON response
- contains `"status":"ok"`

Fail:

- refused / timeout
- non-JSON
- HTTP 5xx

### 2.2 Sentinel list

```bash
curl -sS "$COORD/api/sentinels"
```

Pass:

- JSON response
- contains sentinel summaries
- each summary includes at least:
  - `id`
  - `hostname`
  - `version`
  - `agent_count`
  - `uptime_seconds`
  - `system`

Fail:

- target node absent
- summary shape regresses
- empty list when workers should be connected

## Step 3: Confirm The Target Sentinel Is Visible

If `jq` is available:

```bash
curl -sS "$COORD/api/sentinels" | jq
```

Capture:

- target sentinel `id`
- target `hostname`
- target `agent_count`

Pass:

- the target node from Step 1 is visible in `/api/sentinels`

Fail:

- target has correct on-chain org but is absent from `/api/sentinels`

This split matters:

- Step 1 pass + Step 3 fail: discovery / admission / worker-to-coordinator path issue
- Step 1 fail: cert / registration issue

## Step 4: Execute Remote `status`

Set:

```bash
export TARGET_ID=<sentinel-id-from-step-3>
```

Run:

```bash
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"status"}'
```

Pass:

- contains `"success": true`
- contains non-empty `agents`

Fail:

- sentinel not found
- timeout
- `success: false`
- empty response

If failed, capture:

```bash
grep -E '\[coordinator\]|\[ws\]|\[cmd\]' <coordinator-envd-log> | tail -n 120
grep -E '\[ws\]|\[cmd\]|\[sui\]' <target-envd-log> | tail -n 120
```

## Step 5: Prove It Is Not Self-Loop

Do not use a generic shell payload like `echo hello`. Use a target-specific identity command.

Recommended:

```bash
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"shell","args":"hostname && whoami && uname -a"}'
```

Pass:

- `"success": true`
- output hostname matches the target node, not the coordinator
- output is distinguishable from the coordinator's local machine identity

Fail:

- output matches the coordinator instead of the target
- output is empty or ambiguous

If hostname alone is not unique enough, add a target-side marker first, for example:

```bash
echo trinity-kr6-marker > /tmp/kr6-target-marker
```

Then verify remotely:

```bash
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"shell","args":"cat /tmp/kr6-target-marker"}'
```

## Step 6: Execute One Stronger Control Action

Pick one:

### Option A: Remote shell

```bash
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"shell","args":"tmux ls"}'
```

Pass:

- `"success": true`
- output contains the target node's tmux sessions

### Option B: Remote logs

Pick an agent session from `status` first:

```bash
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"logs","agent_id":"<agent-session>","args":"20"}'
```

Pass:

- `"success": true`
- output contains non-empty logs from the intended target agent

### Option C: `tmux send-keys`

If the target node has a known tmux session:

```bash
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"shell","args":"tmux send-keys -t <session> \"echo kr6-injected\" C-m && sleep 1 && tmux capture-pane -t <session> -p | tail -n 5"}'
```

Pass:

- command succeeds
- captured pane shows `kr6-injected`

This is the strongest proof that control landed on the intended target session rather than just returning a local shell.

## Failure Branches

### Branch A: Org mismatch

Symptoms:

- target logs show `[sui] peer registered on-chain`
- discovered `cert_id` exists
- on-chain cert `org_id` is not the SuLabs org

Check first:

```bash
sui client object <cert-id> --json
ls -l <target-sui-key-path>
sed -n '/^sui:/,/^[^[:space:]]/p' <target-sentinel.yaml>
```

Most likely cause:

- local signer reused an older certificate from another org because `cert_id` was left blank

### Branch B: Peer visible but unreachable

Symptoms:

- target appears in `/api/sentinels`
- remote `status` or `shell` times out or returns failure

Check first:

```bash
grep -E '\[ws\]|\[cmd\]' <coordinator-envd-log> | tail -n 120
grep -E '\[ws\]|\[cmd\]' <target-envd-log> | tail -n 120
curl -sS "$COORD/api/sentinels/$TARGET_ID"
```

Most likely cause:

- worker is listed but command WebSocket path is stale or reconnecting
- coordinator sees cached node info but target command path is not healthy

### Branch C: Shell lands on wrong node

Symptoms:

- remote shell succeeds
- returned hostname / user / marker belongs to coordinator or another machine

Check first:

```bash
hostname
whoami
curl -sS "$COORD/api/sentinels"
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"shell","args":"hostname && whoami"}'
```

Most likely cause:

- operator targeted the wrong sentinel id
- self-loop test setup reused the same node for coordinator and worker
- target identity was never pinned before running shell

## Minimum Acceptance Commands

Run these in order:

1. Health

```bash
curl -sS "$COORD/api/health"
```

2. Sentinel visibility

```bash
curl -sS "$COORD/api/sentinels"
```

3. Remote `status`

```bash
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"status"}'
```

4. Remote `shell` or `tmux send-keys`

```bash
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"shell","args":"hostname && whoami && uname -a"}'
```

or

```bash
curl -sS -X POST "$COORD/api/sentinels/$TARGET_ID/command" \
  -H 'Content-Type: application/json' \
  -d '{"command":"shell","args":"tmux send-keys -t <session> \"echo kr6-injected\" C-m && sleep 1 && tmux capture-pane -t <session> -p | tail -n 5"}'
```

## Minimum Evidence Package

Collect all of the following:

1. Target-side registration evidence
   - `[sui] address=...`
   - `[sui] found existing AgentCertificate: ...`
   - `[sui] peer registered on-chain`
   - on-chain proof that the cert belongs to the expected SuLabs `org_id`

2. Admin-side API evidence
   - raw `GET /api/health` output
   - raw `GET /api/sentinels` output

3. Non-self target command evidence
   - raw remote `status` output with non-empty `agents`
   - raw remote `shell` or `logs` or `tmux send-keys` output
   - target identity proof showing it is not self-loop

4. Correlated logs
   - coordinator-side `[coordinator] / [ws] / [cmd]` lines
   - target-side `[sui] / [ws] / [cmd]` lines

5. Preferred chain artifact
   - `PeerRegistered` event
   - tx hash
   - explorer link or screenshot

## Decision Rule

Mark KR6 non-self target control admission `PASS` only if all are true:

- target cert belongs to the SuLabs `org_id`
- target appears in admin `/api/sentinels`
- remote `status` succeeds against that target
- remote `shell`, `logs`, or `tmux send-keys` succeeds against that target
- target identity evidence proves the command did not land on the coordinator itself

If any one is missing, keep KR6 non-self target admission open and report the exact failing branch above.
