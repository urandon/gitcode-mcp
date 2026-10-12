package localauthority

const schemaVersion = 1
const applicationID = 0x47434d41

const schemaSQL = `
CREATE TABLE project (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 project_id TEXT NOT NULL UNIQUE CHECK(length(project_id)=36), instance_id TEXT NOT NULL UNIQUE CHECK(length(instance_id)=36),
 manifest TEXT NOT NULL CHECK(length(CAST(manifest AS BLOB))<=4096), generation INTEGER NOT NULL CHECK(generation>0),
 sequence INTEGER NOT NULL CHECK(sequence>=0)
);
CREATE TABLE aliases (kind TEXT PRIMARY KEY CHECK(kind IN('issue','comment','label','milestone','document','link')), next_number INTEGER NOT NULL CHECK(next_number>0));
CREATE TABLE entities (
 entity_id TEXT PRIMARY KEY CHECK(length(entity_id)=36), kind TEXT NOT NULL REFERENCES aliases(kind), alias_number INTEGER NOT NULL CHECK(alias_number>0),
 current_revision INTEGER NOT NULL CHECK(current_revision>0),
 UNIQUE(kind,alias_number),
 FOREIGN KEY(entity_id,current_revision) REFERENCES revisions(entity_id,revision) DEFERRABLE INITIALLY DEFERRED
);
CREATE TABLE revisions (
 entity_id TEXT NOT NULL REFERENCES entities(entity_id), revision INTEGER NOT NULL CHECK(revision>0),
 sequence INTEGER NOT NULL UNIQUE CHECK(sequence>0),
 payload TEXT NOT NULL CHECK(length(CAST(payload AS BLOB))<=516096), payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64), tombstone INTEGER NOT NULL CHECK(tombstone IN(0,1)),
 actor_id TEXT NOT NULL CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 128), recorded_at TEXT NOT NULL CHECK(length(recorded_at) BETWEEN 1 AND 64),
 PRIMARY KEY(entity_id,revision)
);
CREATE TABLE relations (
 link_id TEXT PRIMARY KEY REFERENCES entities(entity_id),
 from_id TEXT NOT NULL, from_revision INTEGER NOT NULL,
 to_id TEXT NOT NULL, to_revision INTEGER NOT NULL, relation TEXT NOT NULL CHECK(relation IN('relates_to','blocks','evidence_for','label','milestone')),
 FOREIGN KEY(from_id,from_revision) REFERENCES revisions(entity_id,revision),
 FOREIGN KEY(to_id,to_revision) REFERENCES revisions(entity_id,revision)
);
CREATE INDEX relations_from ON relations(from_id,relation);
CREATE INDEX relations_to ON relations(to_id,relation);
CREATE TABLE receipts (
 key_hash TEXT PRIMARY KEY CHECK(length(key_hash)=64), intent_hash TEXT NOT NULL CHECK(length(intent_hash)=64), operation_id TEXT NOT NULL UNIQUE CHECK(length(operation_id)=36),
 sequence INTEGER NOT NULL UNIQUE REFERENCES revisions(sequence), receipt TEXT NOT NULL CHECK(length(CAST(receipt AS BLOB))<=8192)
);
CREATE TABLE audit (
 sequence INTEGER PRIMARY KEY REFERENCES receipts(sequence), operation_id TEXT NOT NULL,
 entity_id TEXT NOT NULL CHECK(length(entity_id)=36), revision INTEGER NOT NULL CHECK(revision>0), operation TEXT NOT NULL CHECK(operation IN('issue.create','issue.update','comment.create','comment.update','label.create','label.update','milestone.create','milestone.update','link.create','link.update','link.tombstone','document.create','document.update','document.tombstone')),
 key_hash TEXT NOT NULL CHECK(length(key_hash)=64), intent_hash TEXT NOT NULL CHECK(length(intent_hash)=64), actor_hash TEXT NOT NULL CHECK(length(actor_hash)=64), recorded_at TEXT NOT NULL CHECK(length(recorded_at) BETWEEN 1 AND 64)
);
CREATE TRIGGER immutable_revisions_update BEFORE UPDATE ON revisions BEGIN SELECT RAISE(ABORT,'immutable revision'); END;
CREATE TRIGGER immutable_revisions_delete BEFORE DELETE ON revisions BEGIN SELECT RAISE(ABORT,'immutable revision'); END;
CREATE TRIGGER immutable_receipts_update BEFORE UPDATE ON receipts BEGIN SELECT RAISE(ABORT,'immutable receipt'); END;
CREATE TRIGGER immutable_receipts_delete BEFORE DELETE ON receipts BEGIN SELECT RAISE(ABORT,'immutable receipt'); END;
CREATE TRIGGER immutable_audit_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT,'immutable audit'); END;
CREATE TRIGGER immutable_audit_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT,'immutable audit'); END;
CREATE TRIGGER immutable_identity BEFORE UPDATE OF project_id,instance_id,manifest ON project BEGIN SELECT RAISE(ABORT,'immutable project'); END;
CREATE TRIGGER immutable_entity_identity BEFORE UPDATE OF entity_id,kind,alias_number ON entities BEGIN SELECT RAISE(ABORT,'immutable entity'); END;
CREATE TRIGGER immutable_entities_delete BEFORE DELETE ON entities BEGIN SELECT RAISE(ABORT,'immutable entity'); END;
`
