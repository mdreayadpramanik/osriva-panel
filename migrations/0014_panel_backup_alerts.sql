-- Panel-wide alert rules: metric panel_backup watches the control plane's own
-- backups (written by `osriva-install.sh backup` into settings
-- 'panel.backup.last_success'), so its state has no server.
ALTER TABLE alert_rules DROP CONSTRAINT alert_rules_metric_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_metric_check
    CHECK (metric IN ('cpu', 'memory', 'disk', 'load', 'offline', 'backup', 'panel_backup'));
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_panel_backup_no_target
    CHECK (metric <> 'panel_backup' OR (server_id IS NULL AND group_id IS NULL));

ALTER TABLE alert_states DROP CONSTRAINT alert_states_pkey;
ALTER TABLE alert_states ALTER COLUMN server_id DROP NOT NULL;
ALTER TABLE alert_states ADD CONSTRAINT alert_states_rule_server_key UNIQUE NULLS NOT DISTINCT (rule_id, server_id);
