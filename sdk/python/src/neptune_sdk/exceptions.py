class NeptuneError(Exception):
    """Base exception for Neptune SDK."""


class NeptuneRPCError(NeptuneError):
    """Raised when the JSON-RPC server returns an error response."""

    def __init__(self, code: int, message: str, data: object = None) -> None:
        self.code = code
        self.message = message
        self.data = data
        super().__init__(f"RPC error {code}: {message}")


class NeptuneConnectionError(NeptuneError):
    """Raised when the HTTP connection to Neptune fails."""
