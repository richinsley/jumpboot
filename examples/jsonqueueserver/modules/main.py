import time
import jumpboot
from jumpboot import JSONQueueServer, exposed

class CalculatorService(JSONQueueServer):
    """A simple calculator service that exposes methods to Go."""
    
    @exposed
    async def add(self, x: float, y: float) -> float:
        """Add two numbers."""
        return x + y
    
    @exposed
    async def subtract(self, x: float, y: float) -> float:
        """Subtract y from x."""
        return x - y
    
    @exposed
    async def multiply(self, x: float, y: float) -> float:
        """Multiply two numbers."""
        return x * y
    
    @exposed
    async def divide(self, x: float, y: float) -> float:
        """Divide x by y."""
        if y == 0:
            raise ValueError("Division by zero")
        return x / y
    
    # This method won't be exposed (starts with _)
    async def _internal_calculation(self, values):
        return sum(values)
    
    # This will be exposed automatically
    async def calculate_average(self, values: list) -> float:
        """Calculate the average of a list of numbers."""
        if not values:
            return 0
        return sum(values) / len(values)

# Main entry point
if __name__ == "__main__":
    print("Starting CalculatorService...")
    # The server will automatically expose methods and start
    service = CalculatorService()
    
    # Wait until the server is stopped (by "exit" command or error)
    while service.running:
        time.sleep(1)