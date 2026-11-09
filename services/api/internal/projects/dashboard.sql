WITH visible_projects AS MATERIALIZED (
 SELECT p.* FROM projects p
 WHERE p.org_id=$1 AND p.deleted_at IS NULL
 AND ($3::boolean OR p.owner_id=$2 OR p.visibility='team' OR EXISTS(
  SELECT 1 FROM project_members m WHERE m.org_id=p.org_id AND m.project_id=p.id AND m.user_id=$2))
), visible_runs AS MATERIALIZED (
 SELECT r.id,r.status,r.created_at,r.started_at,r.finished_at,r.credits_spent,
 r.summary,r.expires_at,pr.project_id,p.name AS project_name
 FROM runs r
 LEFT JOIN project_runs pr ON pr.org_id=r.org_id AND pr.run_id=r.id
 LEFT JOIN visible_projects p ON p.id=pr.project_id
 WHERE r.org_id=$1 AND r.created_at >= $5 AND r.created_at < $7
 AND ((pr.project_id IS NULL AND $4::boolean) OR p.id IS NOT NULL)
), measurements AS MATERIALIZED (
 SELECT *,created_at >= $6 AS current_period,
 CASE WHEN status='completed' THEN COALESCE((summary->>'lines_total')::bigint,0) ELSE 0 END AS lines_total,
 CASE WHEN status='completed' THEN COALESCE((summary->>'lines_matched')::bigint,0) ELSE 0 END AS lines_matched,
 CASE WHEN status='completed' THEN COALESCE((summary->>'findings_total')::bigint,0) ELSE 0 END AS findings,
 CASE WHEN status='completed' THEN COALESCE((summary->'findings_by_severity'->>'high')::bigint,0)+COALESCE((summary->'findings_by_severity'->>'critical')::bigint,0) ELSE 0 END AS high_findings,
 CASE WHEN status='completed' AND finished_at>=started_at THEN extract(epoch FROM finished_at-started_at) END AS duration
 FROM visible_runs
), periods AS (
 SELECT period,
 count(id) AS runs,
 count(id) FILTER(WHERE status='completed') AS completed,
 count(id) FILTER(WHERE status='failed') AS failed,
 count(id) FILTER(WHERE status='cancelled') AS cancelled,
 count(id) FILTER(WHERE status='queued') AS queued,
 count(id) FILTER(WHERE status='running') AS running,
 count(id) FILTER(WHERE status='needs_approval') AS needs_approval,
 COALESCE(sum(credits_spent),0) AS credits_spent,
 COALESCE(sum(lines_total),0) AS lines_total,
 COALESCE(sum(lines_matched),0) AS lines_matched,
 COALESCE(sum(findings),0) AS findings,
 COALESCE(sum(high_findings),0) AS high_findings,
 percentile_cont(0.5) WITHIN GROUP(ORDER BY duration) AS median_seconds
 FROM (VALUES(true),(false)) AS windows(period)
 LEFT JOIN measurements ON current_period=period GROUP BY period
), daily AS (
 SELECT (created_at AT TIME ZONE 'America/Sao_Paulo')::date AS day,
 count(*) AS runs,count(*) FILTER(WHERE status='completed') AS completed,
 count(*) FILTER(WHERE status='failed') AS failed,sum(credits_spent) AS credits_spent
 FROM measurements WHERE current_period GROUP BY day
), calendar AS (
 SELECT day::date FROM generate_series(
 ($6::timestamptz AT TIME ZONE 'America/Sao_Paulo')::date,
 ($7::timestamptz AT TIME ZONE 'America/Sao_Paulo')::date,interval '1 day') day
), document_totals AS (
 SELECT count(*) AS documents,
 count(*) FILTER(WHERE u.deleted_at IS NOT NULL OR u.expires_at<=$7 OR u.status IN ('infected','rejected','deleted') OR (u.status='pending' AND u.ticket_expires_at<=$7)) AS unavailable_documents
 FROM project_documents d
 JOIN visible_projects p ON p.org_id=d.org_id AND p.id=d.project_id AND p.archived_at IS NULL
 JOIN uploads u ON u.org_id=d.org_id AND u.id=d.upload_id
 WHERE d.removed_at IS NULL AND d.pending_replacement_id IS NULL
), recent AS (
 SELECT id,COALESCE(project_id::text,'') AS project_id,COALESCE(project_name,'') AS project_name,status,created_at,
 COALESCE(expires_at<=$7,false) AS expired,findings
 FROM measurements WHERE current_period ORDER BY created_at DESC,id DESC LIMIT 8
), attention AS (
 SELECT id,COALESCE(project_id::text,'') AS project_id,COALESCE(project_name,'') AS project_name,status,created_at,
 COALESCE(expires_at<=$7,false) AS expired,findings
 FROM measurements m WHERE current_period AND status IN ('failed','needs_approval')
 AND (expires_at IS NULL OR expires_at>$7)
 AND (project_id IS NULL OR NOT EXISTS(SELECT 1 FROM project_runs newer JOIN runs r ON r.org_id=newer.org_id AND r.id=newer.run_id
 WHERE newer.org_id=$1 AND newer.project_id=m.project_id AND (r.created_at,r.id)>(m.created_at,m.id)))
 ORDER BY CASE status WHEN 'needs_approval' THEN 0 ELSE 1 END,created_at DESC,id DESC LIMIT 5
), project_cards AS (
 SELECT p.id,p.name,p.updated_at,
 (SELECT count(*) FROM project_documents d WHERE d.org_id=p.org_id AND d.project_id=p.id AND d.removed_at IS NULL AND d.pending_replacement_id IS NULL) AS documents,
 latest.status AS last_status,CASE WHEN latest.status='completed' THEN COALESCE((latest.summary->>'findings_total')::bigint,0) ELSE 0 END AS findings
 FROM (SELECT * FROM visible_projects WHERE archived_at IS NULL ORDER BY updated_at DESC,id DESC LIMIT 6) p
 LEFT JOIN LATERAL (
 SELECT r.status,r.summary FROM project_runs pr JOIN runs r ON r.org_id=pr.org_id AND r.id=pr.run_id
 WHERE pr.org_id=p.org_id AND pr.project_id=p.id ORDER BY r.created_at DESC,r.id DESC LIMIT 1
 ) latest ON true
 ORDER BY p.updated_at DESC,p.id DESC
)
SELECT jsonb_build_object(
 'current',(SELECT to_jsonb(periods)-'period' FROM periods WHERE period),
 'previous',(SELECT to_jsonb(periods)-'period' FROM periods WHERE NOT period),
 'portfolio',jsonb_build_object('active',(SELECT count(*) FROM visible_projects WHERE archived_at IS NULL),
  'archived',(SELECT count(*) FROM visible_projects WHERE archived_at IS NOT NULL),
  'documents',(SELECT documents FROM document_totals),'unavailable_documents',(SELECT unavailable_documents FROM document_totals)),
 'series',(SELECT COALESCE(jsonb_agg(jsonb_build_object('date',c.day,'runs',COALESCE(d.runs,0),
  'completed',COALESCE(d.completed,0),'failed',COALESCE(d.failed,0),'credits_spent',COALESCE(d.credits_spent,0)) ORDER BY c.day),'[]'::jsonb) FROM calendar c LEFT JOIN daily d ON d.day=c.day),
 'recent',(SELECT COALESCE(jsonb_agg(to_jsonb(recent)),'[]'::jsonb) FROM recent),
 'attention',(SELECT COALESCE(jsonb_agg(to_jsonb(attention)),'[]'::jsonb) FROM attention),
 'projects',(SELECT COALESCE(jsonb_agg(to_jsonb(project_cards)),'[]'::jsonb) FROM project_cards)
);
