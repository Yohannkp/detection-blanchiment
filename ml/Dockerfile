FROM python:3.13-slim
RUN apt-get update && apt-get install -y --no-install-recommends libgomp1 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY requirements-scoreur.txt .
RUN pip install --no-cache-dir -r requirements-scoreur.txt \
 && python -c "import duckdb; duckdb.sql('INSTALL postgres')"
COPY variables.py grand_livre.py scoreur.py ./
RUN useradd --create-home scoreur && cp -r /root/.duckdb /home/scoreur/ && chown -R scoreur /home/scoreur/.duckdb
USER scoreur
CMD ["python", "scoreur.py"]
