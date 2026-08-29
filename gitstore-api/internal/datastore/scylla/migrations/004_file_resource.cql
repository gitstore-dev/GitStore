CREATE TABLE IF NOT EXISTS files_by_namespace (
    namespace text,
    creation_timestamp timestamp,
    uid uuid,
    name text,
    api_version text,
    kind text,
    generation bigint,
    resource_version text,
    revision text,
    creation_actor text,
    update_timestamp timestamp,
    update_actor text,
    labels map<text,text>,
    annotations map<text,text>,
    owner_references text,
    finalizers list<text>,
    deletion_timestamp timestamp,
    repository_id uuid,
    source_path text,
    git_commit_sha text,
    git_ref text,
    spec text,
    body text,
    status text,
    PRIMARY KEY ((namespace), creation_timestamp, uid)
);
CREATE TABLE IF NOT EXISTS files_by_name (
    namespace text, name text, uid uuid, creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
);
CREATE TABLE IF NOT EXISTS files_by_uid (
    uid uuid, namespace text, creation_timestamp timestamp,
    PRIMARY KEY ((uid))
);
