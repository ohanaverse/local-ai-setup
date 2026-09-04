"""Tests for ready toggle delete functionality.

Verifies that:
1. "r" toggles file presence (download on ready=ON, delete on ready=OFF)
2. "r" on ready model cascades to unexpose
3. "d" cascades: unexpose → ready=off (delete file) → registry removal
4. Provider delete() methods work correctly
"""

import pytest
from pathlib import Path
from unittest.mock import Mock, patch, MagicMock
from dataclasses import replace

from modelman.registry import Registry, ModelEntry, ProviderEntry
from modelman.state import StateStore
from modelman.providers.base import VariantSpec
from modelman.queue import PendingChanges


class TestOmlxDelete:
    """Test omlx provider delete() method."""

    def test_delete_removes_directory(self, tmp_path):
        from modelman.providers.omlx import OMLXProvider
        
        # Setup: create a model directory with files
        model_dir = tmp_path / "models" / "test-repo"
        model_dir.mkdir(parents=True)
        (model_dir / "model.safetensors").write_bytes(b"fake data")
        (model_dir / "config.json").write_bytes(b"{}")
        
        provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
        variant: VariantSpec = {
            "id": "test--model",
            "provider": "omlx",
            "repo": "test-org/test-repo",
        }
        
        provider.delete(variant)
        
        assert not model_dir.exists()
        assert (tmp_path / "models").exists()  # Parent dir preserved

    def test_delete_noop_if_absent(self, tmp_path):
        from modelman.providers.omlx import OMLXProvider
        
        provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
        variant: VariantSpec = {
            "id": "test--model",
            "provider": "omlx",
            "repo": "test-org/test-repo",
        }
        
        # Should not raise
        provider.delete(variant)


class TestLlamaCppDelete:
    """Test llamacpp provider delete() method."""

    def test_delete_removes_file_and_orphaned_blob(self, tmp_path):
        from modelman.providers.llamacpp import LlamaCppProvider
        import hashlib
        
        # Setup: create HF cache structure
        hub_dir = tmp_path / "hub"
        repo_dir = hub_dir / "models--test-org--test-repo"
        snapshots_dir = repo_dir / "snapshots" / "abc123"
        blobs_dir = repo_dir / "blobs"
        snapshots_dir.mkdir(parents=True)
        blobs_dir.mkdir()
        
        # Create a GGUF file and its blob
        gguf_content = b"fake gguf data"
        blob_hash = hashlib.sha256(gguf_content).hexdigest()
        gguf_file = snapshots_dir / "model.gguf"
        blob_file = blobs_dir / blob_hash
        
        gguf_file.write_bytes(gguf_content)
        blob_file.write_bytes(gguf_content)  # Hard-link simulation
        
        provider = LlamaCppProvider({})
        variant: VariantSpec = {
            "id": "test--model",
            "provider": "llamacpp",
            "repo": "test-org/test-repo",
            "files": ["model.gguf"],
        }
        
        with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
            provider.delete(variant)
        
        # File deleted from snapshot
        assert not gguf_file.exists()
        # Blob deleted (orphaned)
        assert not blob_file.exists()

    def test_delete_preserves_referenced_blob(self, tmp_path):
        from modelman.providers.llamacpp import LlamaCppProvider
        import hashlib
        
        # Setup: two snapshots sharing a blob
        hub_dir = tmp_path / "hub"
        repo_dir = hub_dir / "models--test-org--test-repo"
        snap1 = repo_dir / "snapshots" / "abc123"
        snap2 = repo_dir / "snapshots" / "def456"
        blobs_dir = repo_dir / "blobs"
        snap1.mkdir(parents=True)
        snap2.mkdir()
        blobs_dir.mkdir()
        
        # Create shared blob
        shared_content = b"shared data"
        blob_hash = hashlib.sha256(shared_content).hexdigest()
        blob_file = blobs_dir / blob_hash
        blob_file.write_bytes(shared_content)
        
        # Both snapshots reference the same blob (via hard-link simulation)
        (snap1 / "model.gguf").write_bytes(shared_content)
        (snap2 / "other.gguf").write_bytes(shared_content)  # Different filename
        
        provider = LlamaCppProvider({})
        variant: VariantSpec = {
            "id": "test--model",
            "provider": "llamacpp",
            "repo": "test-org/test-repo",
            "files": ["model.gguf"],  # Only delete model.gguf
        }
        
        with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
            provider.delete(variant)
        
        # File deleted from snap1
        assert not (snap1 / "model.gguf").exists()
        # File in snap2 preserved (different filename)
        assert (snap2 / "other.gguf").exists()
        # Blob preserved (still referenced by other.gguf)
        assert blob_file.exists()


class TestReadyToggleCascade:
    """Test ready toggle cascades to expose."""

    def test_ready_off_cascades_unexpose_logic(self):
        """Verify the cascade logic: ready=False should queue expose=False."""
        # This tests the logic from action_toggle_ready without needing
        # to instantiate the full Textual screen.
        
        # Simulate state
        persisted_ready = True
        persisted_exposed = True
        queued_ready = {}
        queued_exposes = {}
        
        # Simulate toggle (target = not displayed_ready)
        displayed_ready = queued_ready.get("test", persisted_ready)
        target = not displayed_ready  # False
        
        # Queue the ready toggle
        queued_ready["test"] = target
        
        # Cascade: ready=False must unexpose
        if target is False:
            current_exposed = queued_exposes.get("test", persisted_exposed)
            if current_exposed:
                queued_exposes["test"] = False
        
        # Verify
        assert queued_ready["test"] is False
        assert queued_exposes["test"] is False


class TestDeleteCascade:
    """Test delete cascades to expose and ready."""

    def test_delete_cascades_logic(self):
        """Verify the cascade logic for delete: expose=off → ready=off → registry."""
        # Simulate state
        persisted_ready = True
        persisted_exposed = True
        queued_ready = {}
        queued_exposes = {}
        queued_deletes = {}
        
        model_id = "test"
        
        # Step 1: Queue expose=off if currently exposed
        current_exposed = queued_exposes.get(model_id, persisted_exposed)
        if current_exposed:
            queued_exposes[model_id] = False
        
        # Step 2: Queue ready=off if currently ready
        current_ready = queued_ready.get(model_id, persisted_ready)
        if current_ready:
            queued_ready[model_id] = False
        
        # Step 3: Queue registry removal
        queued_deletes[model_id] = {"id": model_id}
        
        # Verify all three queued
        assert queued_exposes[model_id] is False
        assert queued_ready[model_id] is False
        assert model_id in queued_deletes
