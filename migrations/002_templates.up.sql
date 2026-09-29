-- Notification templates: stored server-side so clients can reference a template
-- id + a variables map instead of shipping pre-rendered content on every request.
--
-- subject_tmpl is nullable because only email uses it; sms and push put
-- everything in body_tmpl.
--
-- Unique on (name, channel, locale) so clients can namespace templates without
-- collision across channels.

begin;

create table if not exists notification_templates (
    id           uuid primary key,
    name         text not null,
    channel      text not null check (channel in ('sms','email','push')),
    locale       text not null default 'en-US',
    subject_tmpl text null,
    body_tmpl    text not null,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    unique (name, channel, locale)
);
create index if not exists idx_templates_name on notification_templates (name);

commit;
