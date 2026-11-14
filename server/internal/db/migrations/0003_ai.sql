-- A workspace may hold its person's own Gemini key: with it the Studio reads
-- each answered call's text itself (the form, a summary, what to re-check).
alter table workspaces add column gemini_key_sealed bytea;
alter table workspaces add column gemini_model text not null default '';

-- calls.analysis: none (no text), done (the Studio's own checks), ai (a model
-- read the call), failed (the model did not answer; the own checks stand).
-- What the model was not sure of, and why a reading failed.
alter table calls add column doubts jsonb not null default '[]';
alter table calls add column analysis_error text not null default '';
