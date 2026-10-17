-- Zero-pad single-digit-hour times (9:00 -> 09:00) so the HH:MM string
-- compare behind the Today view's evening split is correct. Values that do
-- not match an H:MM shape are left alone; the startup check logs them.
-- Idempotent: after the first run no value matches the GLOB anymore.
UPDATE task_schedules SET start_time = '0' || start_time
    WHERE start_time GLOB '[0-9]:[0-5][0-9]';
UPDATE task_schedules SET end_time = '0' || end_time
    WHERE end_time GLOB '[0-9]:[0-5][0-9]';
