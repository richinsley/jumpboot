import time
import sys
from jumpboot import JSONQueueServer, exposed

class CalculatorService(JSONQueueServer):
    """A simple calculator service that exposes methods to Go."""
    
    @exposed
    async def greet(self) -> str:
        """Greet someone by name."""
        print("Python: Greeting Bob")
        howdy = await self.async_request("Greet", ["Bob"])
        print(f"Python: Received greeting: {howdy}")
        return howdy
    
    @exposed
    async def calculate_with_tax(self, amount: float, state: str) -> float:
        """
        Calculate the total price including tax by calling back to Go for the tax rate.
        
        Args:
            amount: The base amount
            state: The state code (e.g., 'CA', 'NY')
            
        Returns:
            The total amount including tax
        """
        print(f"Python: Calculating tax for ${amount:.2f} in state {state}")
        
        try:
            # Call back to Go to get the tax rate for the state
            print(f"Python: Requesting tax rate for state {state} from Go")
            response = await self.async_request("get_tax_rate", {"state": state})
            print(f"Python: Received response: {response}")
            if "error" in response:
                raise ValueError(f"Error getting tax rate: {response['error']}")
                
            tax_rate = response.get("rate", 0.0)
            print(f"Python: Received tax rate {tax_rate:.4f} from Go")
            
            # Calculate total with tax
            total = amount * (1 + tax_rate)
            print(f"Python: Total with tax: ${total:.2f}")
            
            return total
            
        except Exception as e:
            print(f"Python: Error in calculate_with_tax: {e}")
            raise

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