package localauthority

const schemaVersion = 2
const applicationID = 0x47434d41

func authoritySchemaSQL(version int) string {
	switch version {
	case 1:
		return schemaSQL
	case 2:
		return schemaSQL + knowledgeSchemaSQL
	default:
		return ""
	}
}

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

// The v1 catalog above remains byte-for-byte its original contract, pinned by
// knowledge_compatibility_test.go. Only fresh explicit initialization creates
// v2; opening v1 never executes this DDL or silently migrates any stored bytes.
// Planned OIDs do not authorize ref publication without a verified binding CAS.
const knowledgeSchemaSQL = `
CREATE TABLE knowledge_setup (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 project_id TEXT NOT NULL REFERENCES project(project_id), tracker_instance TEXT NOT NULL REFERENCES project(instance_id),
 knowledge_instance TEXT NOT NULL UNIQUE CHECK(length(knowledge_instance)=36),
 coordinate_hash TEXT NOT NULL CHECK(length(coordinate_hash)=64), physical_hash TEXT NOT NULL CHECK(length(physical_hash) IN(0,64)),
 config_hash TEXT NOT NULL CHECK(length(config_hash)=64), algorithm INTEGER NOT NULL CHECK(algorithm=1),
 key_hash TEXT NOT NULL UNIQUE CHECK(length(key_hash)=64), intent_hash TEXT NOT NULL CHECK(length(intent_hash)=64),
 phase TEXT NOT NULL CHECK(phase IN('reserved','bound','ready')), generation INTEGER NOT NULL,
 CHECK((phase='reserved' AND generation=1 AND physical_hash='') OR (phase='bound' AND generation=2 AND length(physical_hash)=64) OR (phase='ready' AND generation=3 AND length(physical_hash)=64))
);
CREATE TABLE document_operations (
 key_hash TEXT PRIMARY KEY CHECK(length(key_hash)=64), intent_hash TEXT NOT NULL CHECK(length(intent_hash)=64),
 operation_id TEXT NOT NULL UNIQUE CHECK(length(operation_id)=36), entity_id TEXT NOT NULL CHECK(length(entity_id)=36), alias_number INTEGER NOT NULL CHECK(alias_number>0),
 operation TEXT NOT NULL CHECK(operation IN('document.create','document.update','document.tombstone')),
 metadata TEXT NOT NULL CHECK(length(CAST(metadata AS BLOB))<=4096),
 documents_oid TEXT NOT NULL CHECK(length(documents_oid)=64), root_oid TEXT NOT NULL CHECK(length(root_oid)=64),
 actor_id TEXT NOT NULL CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 128), recorded_at TEXT NOT NULL CHECK(length(recorded_at) BETWEEN 1 AND 64),
 lane INTEGER NOT NULL CHECK(lane=1), phase TEXT NOT NULL CHECK(phase IN('reserved','bound','settled')), generation INTEGER NOT NULL,
 result TEXT CHECK(result IS NULL OR length(CAST(result AS BLOB))<=8192),
 pin_cleanup TEXT NOT NULL CHECK(pin_cleanup IN('retained','cleared')),
 CHECK((phase='reserved' AND generation=1 AND result IS NULL) OR (phase='bound' AND generation=2 AND result IS NULL) OR (phase='settled' AND generation=3 AND result IS NOT NULL)),
 CHECK(pin_cleanup='retained' OR phase='settled')
);
CREATE UNIQUE INDEX document_operation_lane ON document_operations(lane) WHERE phase<>'settled';
CREATE TRIGGER immutable_knowledge_setup_identity BEFORE UPDATE OF singleton,project_id,tracker_instance,knowledge_instance,coordinate_hash,config_hash,algorithm,key_hash,intent_hash ON knowledge_setup BEGIN SELECT RAISE(ABORT,'immutable knowledge identity'); END;
CREATE TRIGGER monotonic_knowledge_setup BEFORE UPDATE OF physical_hash,phase,generation ON knowledge_setup WHEN NOT ((OLD.phase='reserved' AND NEW.phase='bound' AND NEW.generation=2 AND length(NEW.physical_hash)=64) OR (OLD.phase='bound' AND NEW.phase='ready' AND NEW.generation=3 AND NEW.physical_hash=OLD.physical_hash)) BEGIN SELECT RAISE(ABORT,'knowledge setup phase conflict'); END;
CREATE TRIGGER immutable_knowledge_setup_delete BEFORE DELETE ON knowledge_setup BEGIN SELECT RAISE(ABORT,'immutable knowledge setup'); END;
CREATE TRIGGER immutable_document_operation_identity BEFORE UPDATE OF key_hash,intent_hash,operation_id,entity_id,alias_number,operation,metadata,documents_oid,root_oid,actor_id,recorded_at,lane ON document_operations BEGIN SELECT RAISE(ABORT,'immutable document operation'); END;
CREATE TRIGGER monotonic_document_operation BEFORE UPDATE OF phase,generation,result ON document_operations WHEN NOT ((OLD.phase='reserved' AND NEW.phase='bound' AND NEW.generation=2 AND NEW.result IS NULL) OR (OLD.phase='bound' AND NEW.phase='settled' AND NEW.generation=3 AND NEW.result IS NOT NULL)) BEGIN SELECT RAISE(ABORT,'document operation phase conflict'); END;
CREATE TRIGGER monotonic_document_pin_cleanup BEFORE UPDATE OF pin_cleanup ON document_operations WHEN NOT (OLD.phase='settled' AND NEW.phase='settled' AND OLD.pin_cleanup='retained' AND NEW.pin_cleanup='cleared') BEGIN SELECT RAISE(ABORT,'document pin cleanup conflict'); END;
CREATE TRIGGER immutable_document_operation_delete BEFORE DELETE ON document_operations BEGIN SELECT RAISE(ABORT,'immutable document operation'); END;
`
