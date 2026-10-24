-- name: NextSequence :one
INSERT INTO app.conversation_sequences (sender,next_sequence) VALUES ($1,1)
ON CONFLICT (sender) DO UPDATE SET next_sequence=app.conversation_sequences.next_sequence+1
RETURNING next_sequence;

-- name: InsertEvent :execrows
INSERT INTO app.inbound_events (id, provider_event_id, sender, sequence, content, digest, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (provider_event_id) DO NOTHING;

-- name: GetEventDigest :one
SELECT digest FROM app.inbound_events WHERE provider_event_id = $1;

-- name: InsertJob :exec
INSERT INTO app.jobs (id,event_id,state,due_at) VALUES ($1,$2,'queued',$3);

-- name: ClaimJob :one
WITH picked AS (
 SELECT q.id FROM app.jobs q JOIN app.inbound_events i ON i.id=q.event_id
 WHERE ((q.state='queued' AND q.due_at <= sqlc.arg(now_at)) OR (q.state='leased' AND q.lease_until <= sqlc.arg(now_at)))
 AND NOT EXISTS (
  SELECT 1 FROM app.jobs previous JOIN app.inbound_events pe ON pe.id=previous.event_id
  WHERE pe.sender=i.sender AND pe.sequence<i.sequence AND previous.state IN ('queued','leased')
 )
 ORDER BY q.due_at,q.id FOR UPDATE OF q SKIP LOCKED LIMIT 1
)
UPDATE app.jobs j SET state='leased', generation=j.generation+1, attempt=j.attempt+1, lease_until=sqlc.arg(until_at)
FROM picked p, app.inbound_events e WHERE j.id=p.id AND e.id=j.event_id
RETURNING j.id,j.generation,j.attempt,e.provider_event_id,e.sender,e.content;

-- name: SetJobState :execrows
UPDATE app.jobs SET state=sqlc.arg(state)::text, due_at=sqlc.arg(due_at), lease_until=NULL
WHERE id=sqlc.arg(id) AND generation=sqlc.arg(generation) AND state='leased' AND lease_until > sqlc.arg(now_at);

-- name: InsertOutbox :exec
INSERT INTO app.outbox (id,event_id,content,state,due_at)
SELECT sqlc.arg(id)::uuid, j.event_id, sqlc.arg(content)::text, 'pending', sqlc.arg(due_at)::timestamptz FROM app.jobs j WHERE j.id=sqlc.arg(job_id);

-- name: ExpireSends :many
UPDATE app.outbox SET state='unknown',lease_until=NULL
WHERE state='sending' AND lease_until <= $1 RETURNING id,attempt;

-- name: ClaimDelivery :one
WITH picked AS (
 SELECT q.id FROM app.outbox q JOIN app.inbound_events i ON i.id=q.event_id
 WHERE q.state='pending' AND q.due_at <= sqlc.arg(now_at)
 AND NOT EXISTS (
  SELECT 1 FROM app.outbox previous JOIN app.inbound_events pe ON pe.id=previous.event_id
  WHERE pe.sender=i.sender AND pe.sequence<i.sequence AND previous.state IN ('pending','sending','unknown')
 )
 ORDER BY q.due_at,q.id FOR UPDATE OF q SKIP LOCKED LIMIT 1
)
UPDATE app.outbox o SET state='sending',generation=o.generation+1,attempt=o.attempt+1,lease_until=sqlc.arg(until_at)
FROM picked p,app.inbound_events e WHERE o.id=p.id AND e.id=o.event_id
RETURNING o.id,o.generation,o.attempt,o.content,e.provider_event_id;

-- name: InsertSendAttempt :exec
INSERT INTO app.send_attempts (outbox_id,attempt,generation,started_at,outcome) VALUES ($1,$2,$3,$4,'sending');

-- name: SetDeliveryState :execrows
UPDATE app.outbox SET state=sqlc.arg(state)::text,provider_id=sqlc.arg(provider_id),lease_until=NULL
WHERE id=sqlc.arg(id) AND generation=sqlc.arg(generation) AND state='sending' AND lease_until > sqlc.arg(now_at);

-- name: FinishSendAttempt :exec
UPDATE app.send_attempts SET outcome=$3,provider_id=$4,finished_at=$5 WHERE outbox_id=$1 AND attempt=$2;

-- name: NextDue :one
SELECT min(at)::timestamptz AS due_at FROM (
 SELECT j.due_at AS at FROM app.jobs j JOIN app.inbound_events i ON i.id=j.event_id
 WHERE j.state='queued' AND NOT EXISTS (
  SELECT 1 FROM app.jobs previous JOIN app.inbound_events pe ON pe.id=previous.event_id
  WHERE pe.sender=i.sender AND pe.sequence<i.sequence AND previous.state IN ('queued','leased')
 )
 UNION ALL SELECT lease_until FROM app.jobs WHERE state='leased'
 UNION ALL
 SELECT o.due_at FROM app.outbox o JOIN app.inbound_events i ON i.id=o.event_id
 WHERE o.state='pending' AND NOT EXISTS (
  SELECT 1 FROM app.outbox previous JOIN app.inbound_events pe ON pe.id=previous.event_id
  WHERE pe.sender=i.sender AND pe.sequence<i.sequence AND previous.state IN ('pending','sending','unknown')
 )
 UNION ALL SELECT lease_until FROM app.outbox WHERE state='sending'
) work;

-- name: InspectEvent :one
SELECT e.provider_event_id,e.sender,e.content,j.state AS job_state,j.attempt,
 coalesce(o.state,'')::text AS outbox_state,coalesce(o.provider_id,'')::text AS provider_id,
 (SELECT count(*) FROM app.jobs WHERE event_id=e.id) AS job_count,
 (SELECT count(*) FROM app.outbox WHERE event_id=e.id) AS outbox_count
FROM app.inbound_events e JOIN app.jobs j ON j.event_id=e.id LEFT JOIN app.outbox o ON o.event_id=e.id WHERE e.provider_event_id=$1;
